package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
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
	// A file that declares a package its directory's other files do not shares
	// no build with them: the go command builds a directory's files as one
	// package, and keeps such a file out of every build -- the generator behind
	// //go:build ignore, the tools file in its own package. Hiding the other
	// package's files is what lets that file be read instead of refused.
	hide := otherPackage(path)
	overlay, cleanup, err := hideOverlay(hide)
	if err != nil {
		x.errs = append(x.errs, fmt.Sprintf("%s: %v", x.rel(path), err))
		return
	}
	defer cleanup()
	tried := 0
	var loadErrs []string
	for _, ctx := range contexts(path) {
		if tried == maxBuildsPerFile {
			break
		}
		// A sibling needing the same build has already read this package, or
		// already failed to; either way that load's errors are this file's.
		key := ctx.key() + " " + pkgDir
		if len(hide) > 0 {
			// Another package's files are hidden from this load, so it stands
			// apart from the one the directory's own files share.
			key += " hiding for " + path
		}
		if errs, done := x.loaded[key]; done {
			loadErrs = append(loadErrs, errs...)
			continue
		}
		tried++
		errs := x.loadBuild(dir, ctx, pattern, overlay)
		x.loaded[key] = errs
		loadErrs = append(loadErrs, errs...)
		if x.seen[path] {
			return
		}
	}
	if len(loadErrs) > 0 {
		// A build does include the file; it just does not type-check. The
		// loader's own message names the file and the symbol it could not
		// resolve, which is the answer, where extracting from the partial type
		// information it reports anyway would record a guess.
		x.errs = append(x.errs, loadErrs...)
		return
	}
	x.errs = append(x.errs, fmt.Sprintf("%s: %s: no build this tool can load includes the file, so a rule, table or registration declared in it would be missing from the manifest", x.rel(path), buildConstraint(path)))
}

// loadBuild reads pattern in the build ctx describes and adds what it declares
// to x. It returns the errors the load reported, which the caller reports
// against the file that made it try: a build the tool cannot read leaves that
// file unread, and reporting the loader's message says why. overlay, when set,
// is a go list flag that removes the files the directory cannot build with.
func (x *extractor) loadBuild(dir string, ctx buildContext, pattern, overlay string) []string {
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   dir,
		Tests: true,
		Env:   append(os.Environ(), "GOOS="+ctx.goos, "GOARCH="+ctx.goarch),
	}
	if len(ctx.tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(ctx.tags, ",")}
	}
	if overlay != "" {
		cfg.BuildFlags = append(cfg.BuildFlags, overlay)
	}
	pkgs, err := loadPackages(cfg, pattern)
	if err != nil {
		return []string{err.Error()}
	}
	x.extract(pkgs)
	return nil
}

// otherPackage returns the files in path's directory that declare a package
// path's does not, when there are any. A directory's non-test files are one
// package to the go command, so a file whose package clause differs from a
// sibling's is in no build that includes the sibling: it is the generator kept
// out of every build by //go:build ignore, or the tools file tagged for go mod
// tidy in a package of its own. go build ./... and go vet ./... accept the
// directory, so the module is valid; the file's declarations are still source,
// so the tool reads it with the ones it cannot be built with hidden rather than
// refusing the module or dropping what it declares.
//
// A test file being read is exempt: package foo_test beside package foo is how
// the go command spells an external test package, and the two are read in one
// load. Test files of the other package are hidden like its others, because go
// list reports a package clash against them too.
func otherPackage(path string) (hide []string) {
	pkg, err := packageClause(path)
	if err != nil || pkg == "" || strings.HasSuffix(filepath.Base(path), "_test.go") {
		return nil
	}
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		sibling := filepath.Join(dir, e.Name())
		if e.IsDir() || !isGoFile(e.Name()) || sibling == path {
			continue
		}
		if other, err := packageClause(sibling); err == nil && other != pkg {
			hide = append(hide, sibling)
		}
	}
	return hide
}

// isGoFile reports whether name is a Go file the go command reads, rather than
// one it ignores for its name.
func isGoFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasPrefix(name, "_") && !strings.HasPrefix(name, ".")
}

// packageClause returns the package name a Go file declares.
func packageClause(path string) (string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly)
	if err != nil {
		return "", err
	}
	return f.Name.Name, nil
}

// hideOverlay writes a go list overlay that removes hide from its directory's
// build, and returns the flag that applies it and a func removing the file. An
// empty backing file is how the go command spells a file that does not exist.
func hideOverlay(hide []string) (flag string, cleanup func(), err error) {
	if len(hide) == 0 {
		return "", func() {}, nil
	}
	replace := make(map[string]string, len(hide))
	for _, path := range hide {
		replace[path] = ""
	}
	data, err := json.Marshal(struct {
		Replace map[string]string
	}{replace})
	if err != nil {
		return "", func() {}, err
	}
	f, err := os.CreateTemp("", "cairn-manifest-overlay-*.json")
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", func() {}, fmt.Errorf("writing the overlay: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", func() {}, fmt.Errorf("writing the overlay: %w", err)
	}
	return "-overlay=" + name, func() { os.Remove(name) }, nil
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
// its //go:build line when it has one, the equivalent line for the legacy
// // +build lines it writes instead, and its name when it states neither.
func buildConstraint(path string) string {
	header, goBuild := buildHeader(path)
	if goBuild != "" {
		return goBuild
	}
	if expr := plusBuildExpr(header); expr != nil {
		return "//go:build " + expr.String()
	}
	return fmt.Sprintf("file name %q", filepath.Base(path))
}

// buildExpr parses path's build constraint as the go command reads it, or
// returns nil when it has none.
func buildExpr(path string) constraint.Expr {
	header, goBuild := buildHeader(path)
	if goBuild != "" {
		expr, err := constraint.Parse(goBuild)
		if err != nil {
			return nil
		}
		return expr
	}
	return plusBuildExpr(header)
}

// fileBuild returns the constraint selecting the builds that include path, as
// the manifest records it, or "" when every build includes the file.
//
// The constraint is the file's own -- the suffix of its name and its build
// line -- rather than the context this run read it in, so the same tree records
// the same membership on every host. A registration that moves from one
// platform's file to another's is then a change -base can see, where the
// whole-module list of registrations alone would be identical.
func fileBuild(path string) string {
	name, line := fileNameExpr(path), buildExpr(path)
	switch {
	case name == nil:
		if line == nil {
			return ""
		}
		return line.String()
	case line == nil:
		return name.String()
	case name.String() == line.String():
		// foo_linux.go with //go:build linux names the same build twice.
		return name.String()
	default:
		return (&constraint.AndExpr{X: name, Y: line}).String()
	}
}

// fileNameExpr returns the constraint a file name states, or nil when it states
// none. The go command ties a file to a build by a _GOOS, _GOARCH or
// _GOOS_GOARCH suffix after a non-empty prefix; linux.go alone is not
// constrained, and foo_linux_test.go is.
func fileNameExpr(path string) constraint.Expr {
	name, _, _ := strings.Cut(filepath.Base(path), ".")
	i := strings.Index(name, "_")
	if i < 0 {
		return nil
	}
	parts := strings.Split(name[i:], "_")
	if n := len(parts); n > 0 && parts[n-1] == "test" {
		parts = parts[:n-1]
	}
	goosTags, goarchTags := knownPlatformTags()
	n := len(parts)
	if n >= 2 && goosTags[parts[n-2]] && goarchTags[parts[n-1]] {
		return &constraint.AndExpr{
			X: &constraint.TagExpr{Tag: parts[n-2]},
			Y: &constraint.TagExpr{Tag: parts[n-1]},
		}
	}
	if n >= 1 && (goosTags[parts[n-1]] || goarchTags[parts[n-1]]) {
		return &constraint.TagExpr{Tag: parts[n-1]}
	}
	return nil
}

// parseBuild parses a constraint the manifest records. The manifest writes the
// expression without the //go:build prefix the go command requires.
func parseBuild(s string) (constraint.Expr, error) {
	return constraint.Parse("//go:build " + s)
}

// orBuilds combines two build constraints for one upcaster version: it is
// registered in the builds either one selects. An empty constraint selects
// every build, so it absorbs the other.
func orBuilds(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	if a == b {
		return a
	}
	ax, errA := parseBuild(a)
	bx, errB := parseBuild(b)
	if errA != nil || errB != nil {
		// Both come from fileBuild, which only returns constraints it parsed.
		return a
	}
	return (&constraint.OrExpr{X: ax, Y: bx}).String()
}

// buildHeader returns the file's header as go/build reads it for a build
// directive, and the //go:build line in it if there is one. The header is the
// leading run of // comments and blank lines, which must be followed by a
// blank line; a //go:build line inside a block comment is not a directive, and
// nothing after the first line that is neither is read. A leading UTF-8 BOM is
// not part of the source, as the go command's reader has it.
func buildHeader(path string) (header []byte, goBuild string) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	src = bytes.TrimPrefix(src, []byte{0xef, 0xbb, 0xbf})
	end, p := 0, src
	ended := false
	inBlock := false
	for len(p) > 0 {
		line := p
		if i := bytes.IndexByte(line, '\n'); i >= 0 {
			line, p = line[:i], p[i+1:]
		} else {
			p = p[len(p):]
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 && !ended { // Blank line
			end = len(src) - len(p)
			continue
		}
		if !bytes.HasPrefix(line, []byte("//")) { // Not comment line
			ended = true
		}
		if !inBlock && goBuild == "" && constraint.IsGoBuild(string(line)) {
			goBuild = string(line)
		}
		for len(line) > 0 {
			if inBlock {
				i := bytes.Index(line, []byte("*/"))
				if i < 0 {
					break
				}
				inBlock = false
				line = bytes.TrimSpace(line[i+2:])
				continue
			}
			if bytes.HasPrefix(line, []byte("//")) {
				break
			}
			if bytes.HasPrefix(line, []byte("/*")) {
				inBlock = true
				line = bytes.TrimSpace(line[2:])
				continue
			}
			// Found non-comment text.
			return src[:end], goBuild
		}
	}
	return src[:end], goBuild
}

// plusBuildExpr ANDs the legacy // +build lines in a file header, one per
// conjunct, as the go command does. It returns nil when the header has none.
func plusBuildExpr(header []byte) constraint.Expr {
	var expr constraint.Expr
	for _, line := range strings.Split(string(header), "\n") {
		line = strings.TrimSpace(line)
		if !constraint.IsPlusBuild(line) {
			continue
		}
		x, err := constraint.Parse(line)
		if err != nil {
			continue
		}
		if expr == nil {
			expr = x
		} else {
			expr = &constraint.AndExpr{X: expr, Y: x}
		}
	}
	return expr
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
			if !isGoFile(name) {
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

var (
	platformTagsOnce sync.Once
	knownOS          map[string]bool
	knownArch        map[string]bool
)

// knownPlatformTags lists the GOOS and GOARCH names the toolchain knows, which
// are the suffixes a file name can use to select a build.
func knownPlatformTags() (map[string]bool, map[string]bool) {
	platformTagsOnce.Do(func() {
		knownOS, knownArch = map[string]bool{}, map[string]bool{}
		for _, pair := range platformPairs() {
			knownOS[pair[0]] = true
			knownArch[pair[1]] = true
		}
	})
	return knownOS, knownArch
}

// unixOS lists the GOOS values the go command satisfies the "unix" build tag
// for. go/build keeps the same table in its internal syslist package, which a
// module cannot import, so TestBuildTagsMatchTheGoCommand compares this copy
// with go/build itself.
var unixOS = map[string]bool{
	"aix":       true,
	"android":   true,
	"darwin":    true,
	"dragonfly": true,
	"freebsd":   true,
	"hurd":      true,
	"illumos":   true,
	"ios":       true,
	"linux":     true,
	"netbsd":    true,
	"openbsd":   true,
	"solaris":   true,
}

// buildTags lists the tags the go command satisfies for one GOOS/GOARCH pair:
// the two names, the OS the GOOS implies, and "unix" for a unix-like GOOS.
// Every other tag a constraint names is free: a build may or may not have it.
func buildTags(goos, goarch string) map[string]bool {
	tags := map[string]bool{goos: true, goarch: true}
	switch goos {
	case "android":
		tags["linux"] = true
	case "ios":
		tags["darwin"] = true
	case "illumos":
		tags["solaris"] = true
	}
	if unixOS[goos] {
		tags["unix"] = true
	}
	return tags
}

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
