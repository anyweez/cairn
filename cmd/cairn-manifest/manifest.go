package main

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

const (
	specPath  = "github.com/anyweez/cairn/spec"
	codecPath = "github.com/anyweez/cairn/codec"
	fsmPath   = "github.com/anyweez/cairn/blocks/fsm"
)

// Manifest is a module's spec as a reviewer reads it.
type Manifest struct {
	Rules  []Rule  `yaml:"rules"`
	Codecs []Codec `yaml:"codecs"`
	Tables []Table `yaml:"tables"`
}

// Rule is one spec.Rule declared as a package-level variable.
type Rule struct {
	ID        string `yaml:"id"`
	Kind      string `yaml:"kind"`
	Status    string `yaml:"status"`
	Statement string `yaml:"statement"`
	Package   string `yaml:"package"`
}

// Codec is one type registered with codec.Register outside a test file: a
// type whose values are stored in the ledger.
type Codec struct {
	// Sum is the sum type the codec encodes, Name the registered name.
	Sum     string `yaml:"sum"`
	Name    string `yaml:"name"`
	Version int    `yaml:"version"`
	Type    string `yaml:"type"`
	// Upcasts lists the versions that have an upcaster to the next one.
	Upcasts []int `yaml:"upcasts,omitempty"`
	// Fields is the JSON shape of the type: one "name: shape" per field, with
	// "?" after the name of an omitempty field. A literal "?" in the name is
	// doubled so it can't be read as that marker.
	Fields []string `yaml:"fields"`
}

func (c Codec) key() string { return c.Sum + " " + c.Name }

// Table is one fsm table declared as a package-level variable.
type Table struct {
	Name  string `yaml:"name"`
	Edges []Edge `yaml:"edges"`
}

// Edge is one allowed transition.
type Edge struct {
	From string `yaml:"from"`
	On   string `yaml:"on"`
	To   string `yaml:"to"`
}

// Extract loads the packages matching patterns in dir, with their tests, and
// returns their manifest. Anything the manifest can't state exactly is an
// error rather than a guess: a rule, table or codec registration whose values
// aren't constants, a registration whose type comes from a generic helper's
// type parameter, or a rule ID declared twice.
//
// A file the current build context excludes is read in a build that includes it,
// so the manifest is the whole module's spec and not the spec of the platform
// the tool happens to run on. Each such build resolves every registered type, so
// a JSON shape that differs between builds is an error naming the type and the
// files that declare it. A file no build this tool can load includes is an
// error naming the file and its constraint, and a file a build includes but
// cannot type-check is an error carrying the loader's message naming the file.
// A package that does not type-check in one of the comparison builds contributes
// no shape observation, so a platform the module is not used on does not refuse
// it.
func Extract(dir string, patterns ...string) (*Manifest, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   dir,
		Tests: true,
	}
	pkgs, err := loadPackages(cfg, patterns...)
	if err != nil {
		return nil, err
	}

	x := &extractor{
		dir: dir, seen: map[string]bool{},
		loadedBuilds: map[string]bool{}, loadErrs: map[string][]string{}, buildErrs: map[string]map[string][]string{},
		inherited: inheritedBuild(),
		ruleAt:    map[string]string{}, codecs: map[string]*Codec{}, codecAt: map[string]string{},
		shapes: map[string]map[string]shape{},
	}
	x.extract(pkgs, x.inherited)
	if len(x.errs) == 0 {
		// A file the inherited build context left out declares nothing the
		// manifest can see, so the build-constrained half of the module is
		// read separately. Each build it reads resolves every registered type,
		// so a shape that depends on the build is an error rather than the
		// host's answer. Skipped when the load already found a declaration it
		// cannot read: the error is the answer, and a second load only spends
		// time on it.
		x.readBuilds(dir, patterns)
		x.readShapeBuilds(dir, patterns)
		x.checkShapes()
	}
	x.upcastsToCodecs()
	if len(x.errs) > 0 {
		// A file read in several builds reports the same load error once per
		// build, and a sibling of a broken file reports the error its package
		// already reported; identical messages are one problem.
		slices.Sort(x.errs)
		x.errs = slices.Compact(x.errs)
		return nil, fmt.Errorf("%s", strings.Join(x.errs, "\n"))
	}

	m := &Manifest{Rules: x.rules, Tables: x.tables, Codecs: []Codec{}}
	for _, c := range x.codecs {
		slices.Sort(c.Upcasts)
		c.Upcasts = slices.Compact(c.Upcasts) // a version upcast twice is replaced, not chained
		m.Codecs = append(m.Codecs, *c)
	}
	if m.Rules == nil {
		m.Rules = []Rule{}
	}
	if m.Tables == nil {
		m.Tables = []Table{}
	}
	slices.SortFunc(m.Rules, func(a, b Rule) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(m.Codecs, func(a, b Codec) int { return cmp.Compare(a.key(), b.key()) })
	slices.SortFunc(m.Tables, func(a, b Table) int { return cmp.Compare(a.Name, b.Name) })
	return m, nil
}

// loadPackages loads patterns and refuses a result the manifest cannot be read
// from: no packages at all, and any package carrying an error. The manifest
// states what the type checker resolved, so a package go/packages reports with
// errors is partial type information and everything read from it would be a
// guess -- an unresolved field renders as "invalid type", and a registration
// the checker could not instantiate has no type arguments at all.
//
// The inherited build's load goes through here, so a module that does not
// type-check on the host is refused rather than half-read. The builds read for a
// file the host's context left out do not: they load the whole pattern set, and
// a package that fails on another platform must not refuse the module (see
// loadBuild).
func loadPackages(cfg *packages.Config, patterns ...string) ([]*packages.Package, error) {
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}
	if len(pkgs) == 0 {
		// go list can fail with its message on stderr and nothing on stdout,
		// and x/tools reports that as no packages and a nil error: a module
		// that declares nothing is indistinguishable from a load that read
		// nothing. A manifest is an error rather than a guess, so ask again
		// in a mode that surfaces the command's error and report its cause.
		cause := &packages.Config{
			Mode:       packages.NeedName | packages.NeedFiles,
			Dir:        cfg.Dir,
			Tests:      cfg.Tests,
			Env:        cfg.Env,
			BuildFlags: cfg.BuildFlags,
		}
		if _, err := packages.Load(cause, patterns...); err != nil {
			return nil, fmt.Errorf("loading packages: %w", err)
		}
		return nil, fmt.Errorf("loading packages: no packages match %s in %s", strings.Join(patterns, " "), cfg.Dir)
	}
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("loading packages:\n  %s", strings.Join(loadErrs, "\n  "))
	}
	return pkgs, nil
}

type upcast struct {
	sum, name string
	from      int
	pos       string
}

// extract adds the declarations in pkgs, resolved in build, to x. A package's
// files appear again in its test variant and a file appears again in every
// build that includes it: each file is read once for its rules and tables, but
// every build's resolution of a registered type is recorded, and the files its
// build context left out are remembered for readBuilds.
func (x *extractor) extract(pkgs []*packages.Package, build string) {
	for _, p := range pkgs {
		if strings.HasSuffix(p.PkgPath, ".test") {
			continue // the generated test main
		}
		// A package that does not type-check resolves names from partial type
		// information, so a rule, table or registration read from it would be a
		// guess: it contributes nothing. The file-reading path reports a build
		// whose load failed when the failing package is the file's own, so this
		// loses no error and never records a partial shape.
		if len(p.Errors) > 0 {
			continue
		}
		for _, name := range p.IgnoredFiles {
			if strings.HasSuffix(name, ".go") {
				x.ignored = append(x.ignored, name)
			}
		}
		for _, f := range p.Syntax {
			name := p.Fset.File(f.Pos()).Name()
			first := !x.seen[name]
			x.seen[name] = true
			x.file(p, f, name, build, first)
		}
	}
}

// shape is one build's resolution of a registered type: what the manifest would
// record, and the files that declare the types it is made of, so two builds
// that disagree can name what differs.
type shape struct {
	sum, name, typ string
	version        int
	fields         []string
	// at is where the registration is written, as file:line.
	at string
	// decls maps every named type reachable from the registered type, and every
	// marshaler that decides how it is written, to the file declaring it.
	decls map[string]string
}

type extractor struct {
	dir     string
	seen    map[string]bool
	ignored []string
	// inherited names the build Extract's first load ran in.
	inherited string
	// loadedBuilds keys the builds already read, so one build costs one load
	// however many files it reads.
	loadedBuilds map[string]bool
	// loadErrs holds, per build and package directory, the errors that build's
	// load reported for the package -- nil when it read it. A sibling file
	// needing the same build costs no second load, and still learns why the
	// build failed.
	loadErrs map[string][]string
	// buildErrs is each loaded build's per-package errors as loadBuild found
	// them, so a build first read for its shapes can be reported later for a
	// file it could not read.
	buildErrs map[string]map[string][]string
	rules     []Rule
	ruleAt    map[string]string
	tables    []Table
	codecs    map[string]*Codec
	codecAt   map[string]string
	upcasts   []upcast
	// shapes holds each build's resolution of a registered type, keyed by the
	// codec's key and then the build, so two builds that disagree are an error.
	shapes map[string]map[string]shape
	errs   []string
}

func (x *extractor) errorf(fset *token.FileSet, pos token.Pos, format string, args ...any) {
	x.errs = append(x.errs, x.position(fset, pos)+": "+fmt.Sprintf(format, args...))
}

func (x *extractor) position(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return fmt.Sprintf("%s:%d", x.rel(p.Filename), p.Line)
}

// rel names path the way the module's own files are named in errors.
func (x *extractor) rel(path string) string {
	if rel, err := filepath.Rel(x.dir, path); err == nil {
		return rel
	}
	return path
}

// file reads one file as build resolves it. first is false for a file another
// build has already read: its rules and tables are recorded once, but its
// registrations are still observed, because the shape they resolve is the
// build's.
func (x *extractor) file(p *packages.Package, f *ast.File, name, build string, first bool) {
	if first {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, ident := range vs.Names {
					obj := p.TypesInfo.Defs[ident]
					if obj == nil {
						continue
					}
					var value ast.Expr
					if i < len(vs.Values) {
						value = vs.Values[i]
					}
					switch {
					case isNamed(obj.Type(), specPath, "Rule"):
						x.rule(p, ident, value)
					case isNamed(obj.Type(), fsmPath, "Table"):
						x.table(p, ident, value)
					}
				}
			}
		}
	}
	// Codecs registered only in tests never write to the ledger.
	if strings.HasSuffix(name, "_test.go") {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			x.codecCall(p, call, build, first)
		}
		return true
	})
}

func (x *extractor) rule(p *packages.Package, ident *ast.Ident, value ast.Expr) {
	lit, ok := value.(*ast.CompositeLit)
	if !ok {
		x.errorf(p.Fset, ident.Pos(), "rule %s must be a spec.Rule literal", ident.Name)
		return
	}
	r := Rule{Package: p.PkgPath, Kind: x.enum(p, lit, "Kind", 0), Status: x.enum(p, lit, "Status", 0)}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			x.errorf(p.Fset, elt.Pos(), "rule %s must name its fields", ident.Name)
			return
		}
		field := kv.Key.(*ast.Ident).Name
		v, ok := x.constant(p, kv.Value)
		if !ok {
			x.errorf(p.Fset, kv.Value.Pos(), "rule %s: %s is not a constant", ident.Name, field)
			return
		}
		switch field {
		case "ID":
			r.ID = constant.StringVal(v)
		case "Statement":
			r.Statement = constant.StringVal(v)
		case "Kind", "Status":
			n, _ := constant.Int64Val(v)
			name := x.enum(p, lit, field, n)
			if field == "Kind" {
				r.Kind = name
			} else {
				r.Status = name
			}
		}
	}
	if r.ID == "" || r.Statement == "" {
		x.errorf(p.Fset, ident.Pos(), "rule %s needs an ID and a statement", ident.Name)
		return
	}
	at := x.position(p.Fset, ident.Pos())
	if prev, dup := x.ruleAt[r.ID]; dup {
		x.errorf(p.Fset, ident.Pos(), "rule ID %s is also declared at %s", r.ID, prev)
		return
	}
	x.ruleAt[r.ID] = at
	x.rules = append(x.rules, r)
}

// enum names value n of spec's Kind or Status type by its constant, as the
// type's String method does: Never is "never".
func (x *extractor) enum(p *packages.Package, lit *ast.CompositeLit, typeName string, n int64) string {
	// A literal written through an alias of spec.Rule has an *types.Alias, not
	// a *types.Named, so unalias before reaching for the package that holds the
	// type's constants. The fallback below covers an incomplete or unnamed
	// type rather than panicking on the assertion.
	named, ok := types.Unalias(p.TypesInfo.TypeOf(lit)).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return fmt.Sprintf("%s(%d)", typeName, n)
	}
	scope := named.Obj().Pkg().Scope()
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok || !isNamed(c.Type(), specPath, typeName) {
			continue
		}
		if v, _ := constant.Int64Val(c.Val()); v == n {
			return strings.ToLower(name)
		}
	}
	return fmt.Sprintf("%s(%d)", typeName, n)
}

func (x *extractor) table(p *packages.Package, ident *ast.Ident, value ast.Expr) {
	name := p.PkgPath + "." + ident.Name
	call, ok := value.(*ast.CallExpr)
	if !ok || !isFunc(p.TypesInfo, call, fsmPath, "New") {
		x.errorf(p.Fset, ident.Pos(), "table %s must be built by a direct fsm.New call", ident.Name)
		return
	}
	if call.Ellipsis.IsValid() {
		x.errorf(p.Fset, call.Pos(), "table %s must list its edges, not pass a slice", ident.Name)
		return
	}
	t := Table{Name: name, Edges: []Edge{}}
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.CompositeLit)
		if !ok {
			x.errorf(p.Fset, arg.Pos(), "table %s: each edge must be an fsm.Edge literal", ident.Name)
			continue
		}
		var e Edge
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				x.errorf(p.Fset, elt.Pos(), "table %s: edges must name their fields", ident.Name)
				break
			}
			v, ok := x.constant(p, kv.Value)
			if !ok {
				x.errorf(p.Fset, kv.Value.Pos(), "table %s: %s is not a constant", ident.Name, kv.Key.(*ast.Ident).Name)
				continue
			}
			switch kv.Key.(*ast.Ident).Name {
			case "From":
				e.From = render(v)
			case "On":
				e.On = render(v)
			case "To":
				e.To = render(v)
			}
		}
		t.Edges = append(t.Edges, e)
	}
	slices.SortFunc(t.Edges, func(a, b Edge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.On, b.On), cmp.Compare(a.To, b.To))
	})
	x.tables = append(x.tables, t)
}

func (x *extractor) codecCall(p *packages.Package, call *ast.CallExpr, build string, first bool) {
	info := p.TypesInfo
	switch {
	case isFunc(info, call, codecPath, "Register"):
		inst, ok := info.Instances[callee(call)]
		if !ok || inst.TypeArgs == nil || inst.TypeArgs.Len() < 2 {
			// A registration the type checker could not instantiate has no
			// entry in Instances, and its type arguments are the only place
			// the stored type and the codec are written. There is nothing to
			// record, so this is an error rather than a zero value.
			if first {
				x.errorf(p.Fset, call.Pos(), "codec.Register: the type checker could not instantiate this call, so the type an entry is stored as cannot be read")
			}
			return
		}
		v, sum := inst.TypeArgs.At(0), inst.TypeArgs.At(1)
		subject := "codec.Register[" + types.TypeString(types.Unalias(v), nil) + "]"
		if first {
			if !x.concrete(p, call.Pos(), v, subject, "the stored shape comes from the caller's type argument") {
				return
			}
			if !x.concrete(p, call.Pos(), sum, subject, "the codec a stored entry names comes from the caller's type argument") {
				return
			}
		}
		name, okName := x.constant(p, call.Args[1])
		version, okVersion := x.constant(p, call.Args[2])
		if !okName || !okVersion {
			if first {
				x.errorf(p.Fset, call.Pos(), "codec.Register[%s]: the name and version must be constants", types.TypeString(v, nil))
			}
			return
		}
		n, _ := constant.Int64Val(version)
		c := &Codec{
			// reflect resolves an alias, so the runtime names the aliased type;
			// resolve both here or a registration through an alias is recorded
			// under a name the running program does not have, and Sum and Type
			// disagree about which one it is.
			Sum:     types.TypeString(types.Unalias(sum), nil),
			Name:    constant.StringVal(name),
			Version: int(n),
			Type:    types.TypeString(types.Unalias(v), nil),
			Fields:  fields(v),
		}
		x.observe(p, call, build, c, v)
		if !first {
			return
		}
		at := x.position(p.Fset, call.Pos())
		if prev, dup := x.codecAt[c.key()+" "+build]; dup {
			x.errorf(p.Fset, call.Pos(), "%s is also registered at %s", c.Name, prev)
			return
		}
		x.codecAt[c.key()+" "+build] = at
		x.codecs[c.key()] = c
	case isMethod(info, call, codecPath, "Codec", "Upcast"):
		if !first {
			return
		}
		// The declaring receiver is *codec.Codec[T] whether the caller names it
		// directly, through an alias, or promotes it from an embedded codec;
		// the selection's own receiver is only whatever the caller spelled.
		recv, _ := methodRecv(info, call)
		named, ok := recv.(*types.Named)
		if !ok || named.TypeArgs().Len() != 1 {
			x.errorf(p.Fset, call.Pos(), "Upcast: cannot read which codec this call registers on")
			return
		}
		sum := named.TypeArgs().At(0)
		if !x.concrete(p, call.Pos(), sum, "Upcast", "the codec this upcaster belongs to comes from the caller's type argument") {
			return
		}
		// A method expression passes the codec as its first argument.
		first := 0
		if info.Selections[call.Fun.(*ast.SelectorExpr)].Kind() == types.MethodExpr {
			first = 1
		}
		name, okName := x.constant(p, call.Args[first])
		from, okFrom := x.constant(p, call.Args[first+1])
		if !okName || !okFrom {
			x.errorf(p.Fset, call.Pos(), "Upcast: the name and version must be constants")
			return
		}
		n, _ := constant.Int64Val(from)
		x.upcasts = append(x.upcasts, upcast{
			sum:  types.TypeString(types.Unalias(sum), nil),
			name: constant.StringVal(name),
			from: int(n),
			pos:  x.position(p.Fset, call.Pos()),
		})
	case unattributedUpcast(info, call):
		if first {
			x.errorf(p.Fset, call.Pos(), "Upcast: %s declares its own Upcast, so the codec it registers on cannot be read; call Upcast on the codec itself", types.TypeString(info.Selections[call.Fun.(*ast.SelectorExpr)].Recv(), nil))
		}
	}
}

// observe records the shape build resolves for a registered type, so that a
// second build resolving it differently is an error rather than the host's
// answer written into the manifest.
func (x *extractor) observe(p *packages.Package, call *ast.CallExpr, build string, c *Codec, v types.Type) {
	s := shape{
		sum: c.Sum, name: c.Name, typ: c.Type, version: c.Version, fields: c.Fields,
		at:    x.position(p.Fset, call.Pos()),
		decls: declarationFiles(p.Fset, v, x.rel),
	}
	if x.shapes[c.key()] == nil {
		x.shapes[c.key()] = map[string]shape{}
	}
	x.shapes[c.key()][build] = s
}

// checkShapes refuses a registered type whose name or JSON shape differs between
// the builds the tool read. The manifest states one shape per codec, so
// committing a build's answer would make the file depend on the host and let
// -base miss a wire change made only on another platform.
func (x *extractor) checkShapes() {
	keys := make([]string, 0, len(x.shapes))
	for key := range x.shapes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		obs := x.shapes[key]
		if len(obs) < 2 {
			continue
		}
		builds := make([]string, 0, len(obs))
		for build := range obs {
			builds = append(builds, build)
		}
		slices.Sort(builds)
		first := obs[builds[0]]
		for _, build := range builds[1:] {
			other := obs[build]
			if first.sum == other.sum && first.typ == other.typ && first.version == other.version && sameFields(first.fields, other.fields) {
				continue
			}
			x.errs = append(x.errs, first.at+": "+shapeConflict(first, builds[0], other, build))
		}
	}
}

// shapeConflict says how two builds disagree about a stored type, naming the
// declarations that differ so the reader knows which file to fix.
func shapeConflict(a shape, buildA string, b shape, buildB string) string {
	var parts []string
	switch {
	case a.sum != b.sum || a.typ != b.typ:
		parts = append(parts, fmt.Sprintf("it is %s in %s and %s in %s", a.typ, buildA, b.typ, buildB))
	case a.version != b.version:
		parts = append(parts, fmt.Sprintf("it is version %d in %s and version %d in %s", a.version, buildA, b.version, buildB))
	default:
		parts = append(parts, differingFields(a.fields, b.fields, buildA, buildB))
	}
	parts = append(parts, differingDeclarations(a, b, buildA, buildB)...)
	return fmt.Sprintf("%s (%s) has a different JSON shape in different builds: %s; a stored type must be the same in every build", a.name, a.typ, strings.Join(parts, "; "))
}

// differingFields names the first field two shapes render differently, matched
// by name because that is what encoding/json decodes by.
func differingFields(a, b []string, buildA, buildB string) string {
	index := func(fields []string) map[string]string {
		out := map[string]string{}
		for _, f := range fields {
			name, _, _ := strings.Cut(f, ": ")
			out[name] = f
		}
		return out
	}
	fa, fb := index(a), index(b)
	names := make([]string, 0, len(fa)+len(fb))
	for name := range fa {
		names = append(names, name)
	}
	for name := range fb {
		if _, ok := fa[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		x, okA := fa[name]
		y, okB := fb[name]
		if okA && okB && x == y {
			continue
		}
		switch {
		case okA && okB:
			return fmt.Sprintf("it records field %s in %s and %s in %s", x, buildA, y, buildB)
		case okA:
			return fmt.Sprintf("it records field %s in %s but not in %s", x, buildA, buildB)
		default:
			return fmt.Sprintf("it records field %s in %s but not in %s", y, buildB, buildA)
		}
	}
	return fmt.Sprintf("it records %q in %s and %q in %s", strings.Join(a, ", "), buildA, strings.Join(b, ", "), buildB)
}

// differingDeclarations names the types, and the files declaring them, that the
// two builds resolve differently.
func differingDeclarations(a, b shape, buildA, buildB string) []string {
	names := make([]string, 0, len(a.decls)+len(b.decls))
	for name := range a.decls {
		names = append(names, name)
	}
	for name := range b.decls {
		if _, ok := a.decls[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var out []string
	for _, name := range names {
		fa, okA := a.decls[name]
		fb, okB := b.decls[name]
		if okA && okB && fa == fb {
			continue
		}
		switch {
		case okA && okB:
			out = append(out, fmt.Sprintf("%s is declared in %s in %s and in %s in %s", name, fa, buildA, fb, buildB))
		case okA:
			out = append(out, fmt.Sprintf("%s is declared in %s in %s and in no file %s reads", name, fa, buildA, buildB))
		default:
			out = append(out, fmt.Sprintf("%s is declared in %s in %s and in no file %s reads", name, fb, buildB, buildA))
		}
		if len(out) == 3 {
			out = append(out, "and more")
			break
		}
	}
	return out
}

func (x *extractor) upcastsToCodecs() {
	for _, u := range x.upcasts {
		c, ok := x.codecs[Codec{Sum: u.sum, Name: u.name}.key()]
		if !ok {
			x.errs = append(x.errs, fmt.Sprintf("%s: upcaster for %s, which is not registered in %s", u.pos, u.name, u.sum))
			continue
		}
		c.Upcasts = append(c.Upcasts, u.from)
	}
}

func (x *extractor) constant(p *packages.Package, e ast.Expr) (constant.Value, bool) {
	tv, ok := p.TypesInfo.Types[e]
	return tv.Value, ok && tv.Value != nil
}

// concrete reports whether t may be recorded as a stored type, and records an
// error when it comes from a generic helper's type parameter. subject names the
// declaration, consequence what is recorded in the parameter's place.
//
// codecCall reads the type arguments written at the call site, and inside a
// generic helper those are the helper's own parameters. A parameter's JSON shape
// is its constraint's — "=: any" for an unconstrained one — so every
// instantiation of the helper would be recorded with that same shape and a wire
// change to the type the caller supplies would pass -base. The manifest states
// what is stored or it is an error, so the registration is refused rather than
// recorded under the parameter.
func (x *extractor) concrete(p *packages.Package, pos token.Pos, t types.Type, subject, consequence string) bool {
	param := typeParam(t)
	if param == "" {
		return true
	}
	x.errorf(p.Fset, pos, "%s: %s is a type parameter, so %s, not %s; register the concrete type where this helper is instantiated", subject, param, consequence, param)
	return false
}

// typeParam names a type parameter that t is or contains, or "" when t is
// concrete: Wrapper[V] and []V hold one without being one.
func typeParam(t types.Type) string {
	return typeParamIn(t, map[types.Type]bool{})
}

func typeParamIn(t types.Type, seen map[types.Type]bool) string {
	t = types.Unalias(t)
	if _, ok := t.(*types.TypeParam); ok {
		return types.TypeString(t, nil)
	}
	if seen[t] {
		return ""
	}
	seen[t] = true
	for _, child := range typeChildren(t) {
		if param := typeParamIn(child, seen); param != "" {
			return param
		}
	}
	return ""
}

// declarations maps every named type reachable from t, and every marshaler
// that decides how it is written, to the file that declares it. Two builds whose
// resolutions disagree name the declarations that differ with it.
func declarationFiles(fset *token.FileSet, t types.Type, rel func(string) string) map[string]string {
	out := map[string]string{}
	seen := map[types.Type]bool{}
	var walk func(types.Type)
	walk = func(t types.Type) {
		// An alias declared behind a constraint points at different types in
		// different builds, and the alias's own name is what the reader has to
		// find, so record it before resolving it away.
		if alias, ok := t.(*types.Alias); ok {
			if obj := alias.Obj(); obj != nil && obj.Pos().IsValid() {
				out[types.TypeString(alias, nil)] = rel(fset.Position(obj.Pos()).Filename)
			}
		}
		t = types.Unalias(t)
		if seen[t] {
			return
		}
		seen[t] = true
		named, ok := t.(*types.Named)
		if !ok {
			for _, child := range typeChildren(t) {
				walk(child)
			}
			return
		}
		name := types.TypeString(named, nil)
		if obj := named.Obj(); obj != nil && obj.Pos().IsValid() {
			out[name] = rel(fset.Position(obj.Pos()).Filename)
		}
		// Whether a type is opaque depends on these two methods alone, and one
		// declared behind a constraint changes the shape without changing the
		// type's own declaration.
		for _, method := range []string{"MarshalJSON", "MarshalText"} {
			for _, recv := range []types.Type{named, types.NewPointer(named)} {
				if obj, _, _ := types.LookupFieldOrMethod(recv, false, nil, method); obj != nil && obj.Pos().IsValid() {
					out[name+"."+method] = rel(fset.Position(obj.Pos()).Filename)
				}
			}
		}
		// A named type's children are its type arguments and its underlying
		// type's fields; typeChildren alone returns only the former.
		walk(named.Underlying())
		args := named.TypeArgs()
		for i := range args.Len() {
			walk(args.At(i))
		}
	}
	walk(t)
	return out
}

// typeChildren lists the types t contains that could hold a type parameter.
func typeChildren(t types.Type) []types.Type {
	switch u := t.(type) {
	case *types.Pointer:
		return []types.Type{u.Elem()}
	case *types.Slice:
		return []types.Type{u.Elem()}
	case *types.Array:
		return []types.Type{u.Elem()}
	case *types.Map:
		return []types.Type{u.Key(), u.Elem()}
	case *types.Named:
		args := u.TypeArgs()
		children := make([]types.Type, args.Len())
		for i := range args.Len() {
			children[i] = args.At(i)
		}
		return children
	case *types.Struct:
		children := make([]types.Type, u.NumFields())
		for i := range u.NumFields() {
			children[i] = u.Field(i).Type()
		}
		return children
	}
	return nil
}

func render(v constant.Value) string {
	if v.Kind() == constant.String {
		return constant.StringVal(v)
	}
	return v.ExactString()
}

func isNamed(t types.Type, pkg, name string) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == pkg && n.Obj().Name() == name
}

// callee is the identifier a call names, through any package qualifier and
// explicit type arguments.
func callee(call *ast.CallExpr) *ast.Ident {
	fun := call.Fun
	for {
		switch f := fun.(type) {
		case *ast.IndexExpr:
			fun = f.X
		case *ast.IndexListExpr:
			fun = f.X
		case *ast.SelectorExpr:
			return f.Sel
		case *ast.Ident:
			return f
		default:
			return nil
		}
	}
}

func isFunc(info *types.Info, call *ast.CallExpr, pkg, name string) bool {
	id := callee(call)
	if id == nil {
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == pkg && fn.Name() == name &&
		fn.Type().(*types.Signature).Recv() == nil
}

// methodRecv returns the receiver type a selected method declares, with an
// alias and a pointer resolved. A selection's receiver is the type the caller
// wrote -- an alias, or the outer type of a promoted method -- so it names a
// codec only for the forms the extractor anticipates. The method's own
// receiver names the same codec however the call is written.
func methodRecv(info *types.Info, call *ast.CallExpr) (types.Type, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	s, ok := info.Selections[sel]
	if !ok {
		return nil, false
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok {
		return nil, false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return nil, false
	}
	r := types.Unalias(sig.Recv().Type())
	if ptr, ok := r.(*types.Pointer); ok {
		r = types.Unalias(ptr.Elem())
	}
	return r, true
}

func isMethod(info *types.Info, call *ast.CallExpr, pkg, recv, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	s, ok := info.Selections[sel]
	if !ok {
		return false
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok {
		return false
	}
	r, ok := methodRecv(info, call)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == pkg && fn.Name() == name && isNamed(r, pkg, recv)
}

// unattributedUpcast reports a call to an Upcast method on a type that embeds a
// codec but does not use the codec's method. A promoted call is the codec's own
// method and was read above. A wrapper that declares its own Upcast hides the
// codec's, and only that method knows what it does with the name and version,
// so the extractor refuses rather than silently records nothing.
func unattributedUpcast(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	s, ok := info.Selections[sel]
	if !ok {
		return false
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok || fn.Name() != "Upcast" {
		return false
	}
	if fn.Pkg() != nil && fn.Pkg().Path() == codecPath {
		return false
	}
	return embedsCodec(s.Recv())
}

// embedsCodec reports whether t carries a *codec.Codec in an embedded field, at
// any depth. It tells a wrapper that declares its own Upcast apart from an
// unrelated type that happens to have a method of that name.
func embedsCodec(t types.Type) bool {
	return embedsCodecIn(t, map[types.Type]bool{})
}

func embedsCodecIn(t types.Type, seen map[types.Type]bool) bool {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	if isNamed(t, codecPath, "Codec") {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		f := st.Field(i)
		if f.Embedded() && embedsCodecIn(f.Type(), seen) {
			return true
		}
	}
	return false
}
