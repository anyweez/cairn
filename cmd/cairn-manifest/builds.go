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
	s := c.goos + "/" + c.goarch
	if len(c.tags) > 0 {
		s += " tags=" + strings.Join(c.tags, ",")
	}
	return s
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
//
// Each such build loads the whole pattern set, not just the file's package, so
// every registered type is resolved in it wherever it is declared: a field
// whose type one platform declares differently is caught rather than recorded
// as the host's shape.
func (x *extractor) readBuilds(dir string, patterns []string) {
	for _, path := range x.unread(dir, patterns) {
		if x.seen[path] {
			continue
		}
		x.readInBuild(dir, path, patterns)
	}
}

// inheritedBuild names the build Extract's first load ran in: the GOOS and
// GOARCH in the environment, which is what packages.Load reads when the tool
// gives it no build configuration of its own.
func inheritedBuild() string {
	goos, goarch := os.Getenv("GOOS"), os.Getenv("GOARCH")
	if goos == "" {
		goos = build.Default.GOOS
	}
	if goarch == "" {
		goarch = build.Default.GOARCH
	}
	return buildContext{goos: goos, goarch: goarch}.key()
}

// shapeBuilds lists the builds whose resolution of a registered type is compared.
// They are a fixed set rather than the host's, so which builds are compared does
// not depend on the machine: the inherited build is compared with them, and a
// file the host includes and another platform excludes -- a marshaler declared
// for one platform, say -- is read from both sides.
func shapeBuilds() []buildContext {
	return []buildContext{
		{goos: "linux", goarch: "amd64"},
		{goos: "darwin", goarch: "arm64"},
		{goos: "windows", goarch: "amd64"},
	}
}

// readShapeBuilds reads the comparison builds when the module declares a file
// whose platform decides whether it is built, since only then can a registered
// type's shape depend on the host. Reading only the builds needed to see an
// excluded file would leave the compared set depending on the host.
func (x *extractor) readShapeBuilds(dir string, patterns []string) {
	if len(x.codecs) == 0 || !x.hasPlatformConstrained(dir, patterns) {
		return
	}
	for _, ctx := range shapeBuilds() {
		if ctx.key() == x.inherited || x.loadedBuilds[ctx.key()] {
			continue
		}
		// A comparison build's errors are not the module's: a package that
		// does not type-check on a platform the module is not used on must not
		// refuse it. It contributes no observation -- extract skips a package
		// that does not type-check -- and the file-reading path reports a failed
		// build only for the file whose own package failed.
		_, _ = x.loadBuild(dir, ctx, patterns)
	}
}

// hasPlatformConstrained reports whether the module declares a file whose
// platform decides whether it is built. A file kept out by another tag is read in a build
// that enables it on every host, so it cannot make the result host-dependent;
// a file behind a GOOS or GOARCH is built on some hosts and not others, and the
// builds compared have to cover both sides.
func (x *extractor) hasPlatformConstrained(dir string, patterns []string) bool {
	var files []string
	if slices.Contains(patterns, "./...") {
		all, err := moduleGoFiles(dir)
		if err != nil {
			return false // readBuilds reports the walk's error
		}
		files = all
	} else {
		files = append(files, x.ignored...)
		for path := range x.seen {
			files = append(files, path)
		}
	}
	for _, path := range files {
		if platformConstrained(path) {
			return true
		}
	}
	return false
}

// platformConstrained reports whether a GOOS or GOARCH decides whether path is
// built. A file kept out by another tag is read in a build that enables it on
// every host, so it cannot make the result host-dependent; a file behind a
// platform is built on some hosts and not others, and the builds compared have
// to cover both sides. The search is the one contexts already does: a file is
// platform-constrained when one tag set selects it for some platforms and not
// others.
func platformConstrained(path string) bool {
	for _, tags := range tagSets(extraTags(path)) {
		ctx := buildContext{tags: tags}
		selected := 0
		for _, pair := range platformPairs() {
			ctx.goos, ctx.goarch = pair[0], pair[1]
			if ctx.selects(path) {
				selected++
			}
		}
		if selected > 0 && selected < len(platformPairs()) {
			return true
		}
	}
	return false
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

// readInBuild reads one file in a build that includes it, and with it the rest
// of the module as that build resolves it.
func (x *extractor) readInBuild(dir, path string, patterns []string) {
	pkgDir := filepath.Dir(path)
	rel, err := filepath.Rel(dir, pkgDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		x.errs = append(x.errs, fmt.Sprintf("%s is outside %s, so no package pattern reaches it", path, dir))
		return
	}
	tried := 0
	var loadErrs []string
	for _, ctx := range contexts(path) {
		if tried == maxBuildsPerFile {
			break
		}
		// A build already read has read every file it selects, and remembers
		// what the file's own package reported if it did not type-check.
		key := ctx.key() + " " + pkgDir
		if errs, done := x.loadErrs[key]; done {
			loadErrs = append(loadErrs, errs...)
			continue
		}
		tried++
		byDir, err := x.loadBuild(dir, ctx, patterns)
		if err != nil {
			x.errs = append(x.errs, err.Error())
			return
		}
		errs := byDir[pkgDir]
		x.loadErrs[key] = errs
		loadErrs = append(loadErrs, errs...)
		if x.seen[path] {
			return
		}
	}
	if len(loadErrs) > 0 {
		// A build does include the file; it just does not type-check. The
		// loader's own message names the file and the symbol it could not
		// resolve, which is the answer, where extracting from the partial type
		// information it reports anyway would record a guess. Only the file's
		// own package is reported: another package in the same load is not this
		// file's failure, and may fail on a platform the module is not used on.
		x.errs = append(x.errs, loadErrs...)
		return
	}
	x.errs = append(x.errs, fmt.Sprintf("%s: %s: no build this tool can load includes the file, so a rule, table or registration declared in it would be missing from the manifest", x.rel(path), buildConstraint(path)))
}

// loadBuild reads patterns in the build ctx describes and adds what it declares
// to x, including every registered type's shape as this build resolves it. It
// returns the errors each package reported, keyed by the directory holding the
// package's files, so a caller reading one file can report the one for that
// file's own package: a build the tool cannot read leaves that file unread, and
// the loader's message says why. The whole pattern set is loaded whether or not
// the file's own package failed, since the other packages' shapes still matter.
//
// A build is loaded once however many files it reads; the second caller gets the
// same per-package errors.
func (x *extractor) loadBuild(dir string, ctx buildContext, patterns []string) (map[string][]string, error) {
	if x.loadedBuilds[ctx.key()] {
		return x.buildErrs[ctx.key()], nil
	}
	x.loadedBuilds[ctx.key()] = true
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   dir,
		Tests: true,
		Env:   append(os.Environ(), "GOOS="+ctx.goos, "GOARCH="+ctx.goarch),
	}
	if len(ctx.tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(ctx.tags, ",")}
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages for %s: %w", ctx.key(), err)
	}
	if len(pkgs) == 0 {
		// Nothing matches the patterns in this build, so a file it should have
		// read stays unread and readInBuild reports it.
		return nil, nil
	}
	x.extract(pkgs, ctx.key())
	byDir := map[string][]string{}
	for _, p := range pkgs {
		if len(p.Errors) == 0 {
			continue
		}
		dir := filepath.Clean(p.Dir)
		for _, e := range p.Errors {
			byDir[dir] = append(byDir[dir], e.Error())
		}
	}
	x.buildErrs[ctx.key()] = byDir
	return byDir, nil
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
// inherited build first and the common targets next, so the build read for a
// file the host excludes is one a module is likely to type-check under. It asks
// `go tool dist list` rather than carrying its own copy of the list, so a
// platform added to a later toolchain is searched for rather than refused.
func platformPairs() [][2]string {
	pairsOnce.Do(func() {
		host := [2]string{build.Default.GOOS, build.Default.GOARCH}
		pairsAll = [][2]string{host}
		seen := map[[2]string]bool{host: true}
		add := func(pair [2]string) {
			if !seen[pair] {
				seen[pair] = true
				pairsAll = append(pairsAll, pair)
			}
		}
		for _, pair := range [][2]string{
			{"linux", "amd64"}, {"linux", "arm64"},
			{"darwin", "arm64"}, {"darwin", "amd64"},
			{"windows", "amd64"}, {"windows", "arm64"},
		} {
			add(pair)
		}
		out, err := exec.Command("go", "tool", "dist", "list").Output()
		if err != nil {
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			goos, goarch, ok := strings.Cut(strings.TrimSpace(line), "/")
			if !ok {
				continue
			}
			add([2]string{goos, goarch})
		}
	})
	return pairsAll
}
