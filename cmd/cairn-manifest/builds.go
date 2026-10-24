package main

import (
	"fmt"
	"go/build"
	"go/build/constraint"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"
)

// maxBuildsPerFile bounds the builds tried for one file before it is reported
// as unreadable. A file's constraint selects the build it belongs to, so the
// first match usually reads it; the bound is here so a tree whose builds all
// fail to load cannot turn one file into a load per platform.
const maxBuildsPerFile = 8

// buildContext is one build of the module: a GOOS/GOARCH pair and the extra
// build tags that, together, select the files it reads.
type buildContext struct {
	goos, goarch string
	tags         []string
}

func (c buildContext) key() string {
	return c.goos + "/" + c.goarch + " tags=" + strings.Join(c.tags, ",")
}

// selects reports whether the file at path belongs to a package under c.
func (c buildContext) selects(path string) bool {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.BuildTags = c.goos, c.goarch, c.tags
	ok, err := ctx.MatchFile(filepath.Dir(path), filepath.Base(path))
	return err == nil && ok
}

// readBuilds reads the module's files that the build context this run inherited
// left out, so that every declaration under a build constraint is either in the
// manifest or an error naming it.
//
// A file selected by a build constraint is part of the module's spec: a codec,
// rule or table declared in one is stored in the ledger on the platform that
// builds it, so a manifest generated on a host that does not build it would
// silently under-state the module -- and the same tree would produce a
// different manifest on each platform. packages.Load reads only the files the
// inherited build selects, so the extractor reads the rest itself: each file is
// read in a build that includes it, found by matching its name and build line
// against every platform the toolchain knows and the extra tags its line names.
func (x *extractor) readBuilds(dir string, patterns []string) {
	for _, path := range x.unread(dir, patterns) {
		if x.seen[path] {
			continue
		}
		x.readInBuild(dir, path)
	}
}

// unread lists the files the module has that no load has read: the packages'
// ignored files, and -- when the whole module was asked for -- every Go file
// under dir. The second is what catches a package whose files are all
// constrained out, which disappears from ./... entirely rather than being named
// in any package's ignored files.
func (x *extractor) unread(dir string, patterns []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		path = filepath.Clean(path)
		if x.seen[path] || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, path := range x.ignored {
		add(path)
	}
	if slices.Contains(patterns, "./...") {
		files, err := moduleGoFiles(dir)
		if err != nil {
			x.errs = append(x.errs, err.Error())
			return out
		}
		for _, path := range files {
			add(path)
		}
	}
	slices.Sort(out)
	return out
}

// readInBuild reads one file in a build that includes it.
func (x *extractor) readInBuild(dir, path string) {
	pkgDir := filepath.Dir(path)
	rel, err := filepath.Rel(dir, pkgDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		x.errs = append(x.errs, fmt.Sprintf("%s is outside %s, so no package pattern reaches it", path, dir))
		return
	}
	pattern := "."
	if rel != "." {
		pattern = "./" + filepath.ToSlash(rel)
	}
	tried := 0
	for _, ctx := range contexts(path) {
		if tried == maxBuildsPerFile {
			break
		}
		// A sibling needing the same build has already read this package.
		key := ctx.key() + " " + pkgDir
		if x.loaded[key] {
			continue
		}
		x.loaded[key] = true
		tried++
		x.loadBuild(dir, ctx, pattern)
		if x.seen[path] {
			return
		}
	}
	x.errs = append(x.errs, fmt.Sprintf("%s: %s: no build this tool can load includes the file, so a rule, table or registration declared in it would be missing from the manifest", x.rel(path), buildConstraint(path)))
}

// loadBuild reads pattern in the build ctx describes and adds what it declares
// to x. A build that fails to load leaves the files it was meant to read
// unread, which the caller reports.
func (x *extractor) loadBuild(dir string, ctx buildContext, pattern string) {
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   dir,
		Tests: true,
		Env:   append(os.Environ(), "GOOS="+ctx.goos, "GOARCH="+ctx.goarch),
	}
	if len(ctx.tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(ctx.tags, ",")}
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		return
	}
	x.extract(pkgs)
}

// contexts lists the builds that include path, the inherited build first, so a
// file the inherited build left out is read in the build it belongs to.
func contexts(path string) []buildContext {
	tags := extraTags(path)
	var out []buildContext
	for _, pair := range platformPairs() {
		for _, set := range tagSets(tags) {
			c := buildContext{goos: pair[0], goarch: pair[1], tags: set}
			if c.selects(path) {
				out = append(out, c)
			}
		}
	}
	return out
}

// tagSets lists every subset of tags, none first, so the search can enable the
// tags a file's build line requires without inventing ones it does not.
func tagSets(tags []string) [][]string {
	sets := [][]string{nil}
	for i, tag := range tags {
		if i >= maxSearchTags {
			break
		}
		for _, set := range slices.Clone(sets) {
			sets = append(sets, append(slices.Clone(set), tag))
		}
	}
	return sets
}

// maxSearchTags bounds the tags enabled together: a build line naming more than
// this many is past what the search tries.
const maxSearchTags = 3

// extraTags lists the build tags, other than a platform's, that path's build
// line names: "tools" on a tools file, "ignore" on a generator kept out of every
// build, "pgx" on a feature-tagged one. They are the tags the search must enable
// for the file to be part of any build at all.
func extraTags(path string) []string {
	expr := buildExpr(path)
	if expr == nil {
		return nil
	}
	var tags []string
	var walk func(constraint.Expr)
	walk = func(e constraint.Expr) {
		switch e := e.(type) {
		case *constraint.TagExpr:
			if !isPlatformTag(e.Tag) {
				tags = append(tags, e.Tag)
			}
		case *constraint.NotExpr:
			walk(e.X)
		case *constraint.AndExpr:
			walk(e.X)
			walk(e.Y)
		case *constraint.OrExpr:
			walk(e.X)
			walk(e.Y)
		}
	}
	walk(expr)
	slices.Sort(tags)
	return slices.Compact(tags)
}

// isPlatformTag reports whether the go command sets tag for the build itself,
// so enabling it by hand would say nothing about the file.
func isPlatformTag(tag string) bool {
	switch tag {
	case "unix", "cgo", "gc", "gccgo", "boringcrypto", "purego":
		return true
	}
	// A release tag (go1.26) or an architecture feature (amd64.v2).
	if strings.HasPrefix(tag, "go1.") || strings.Contains(tag, ".") {
		return true
	}
	for _, pair := range platformPairs() {
		if pair[0] == tag || pair[1] == tag {
			return true
		}
	}
	return false
}

// buildConstraint names the constraint that keeps path out of this run's build:
// its //go:build line when it has one, and its name otherwise, which is where
// the go command reads the rest of them.
func buildConstraint(path string) string {
	if line := buildLine(path); line != "" {
		return line
	}
	return fmt.Sprintf("file name %q", filepath.Base(path))
}

// buildExpr parses path's build constraint, or returns nil when it has none.
func buildExpr(path string) constraint.Expr {
	line := buildLine(path)
	if line == "" {
		return nil
	}
	expr, err := constraint.Parse(line)
	if err != nil {
		return nil
	}
	return expr
}

// buildLine returns path's //go:build line, or the legacy // +build line
// before it, as it is written. Every comment line is scanned rather than the
// header alone: a line that parses as a build constraint is one, and the search
// it feeds verifies the file itself before anything is read from it.
func buildLine(path string) string {
	src, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "//") {
			continue
		}
		if _, err := constraint.Parse(line); err == nil {
			return line
		}
	}
	return ""
}

// moduleGoFiles lists every Go file in the module rooted at dir, skipping the
// directories the go command never reads: testdata, vendor, names beginning
// with "." or "_", and nested modules, which are separate modules with manifests
// of their own. Files the go command ignores for their name are skipped too.
func moduleGoFiles(dir string) ([]string, error) {
	var out []string
	var walk func(path string) error
	walk = func(path string) error {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if path != dir {
			for _, e := range entries {
				if e.Name() == "go.mod" && e.Type().IsRegular() {
					return nil
				}
			}
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
					continue
				}
				if err := walk(filepath.Join(path, name)); err != nil {
					return err
				}
				continue
			}
			if !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			out = append(out, filepath.Join(path, name))
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return nil, fmt.Errorf("walking %s: %w", dir, err)
	}
	return out, nil
}

var (
	pairsOnce sync.Once
	pairsAll  [][2]string
)

// platformPairs lists the GOOS/GOARCH pairs the toolchain can target, the
// inherited build first. It asks `go tool dist list` rather than carrying its
// own copy of the list, so a platform added to a later toolchain is searched
// for rather than refused.
func platformPairs() [][2]string {
	pairsOnce.Do(func() {
		host := [2]string{build.Default.GOOS, build.Default.GOARCH}
		pairsAll = [][2]string{host}
		out, err := exec.Command("go", "tool", "dist", "list").Output()
		if err != nil {
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			goos, goarch, ok := strings.Cut(strings.TrimSpace(line), "/")
			if !ok || (goos == host[0] && goarch == host[1]) {
				continue
			}
			pairsAll = append(pairsAll, [2]string{goos, goarch})
		}
	})
	return pairsAll
}
