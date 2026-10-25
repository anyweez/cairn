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
// the tool happens to run on. A file no build this tool can load includes is an
// error naming the file and its constraint.
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
		dir: dir, seen: map[string]bool{}, loaded: map[string][]string{},
		ruleAt: map[string]string{}, codecs: map[string]*Codec{}, codecAt: map[string]string{},
	}
	x.extract(pkgs)
	if len(x.errs) == 0 {
		// A file the inherited build context left out declares nothing the
		// manifest can see, so the build-constrained half of the module is
		// read separately. Skipped when the load already found a declaration
		// it cannot read: the error is the answer, and a second load only
		// spends time on it.
		x.readBuilds(dir, patterns)
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
// Both loads the tool performs go through here: the inherited build's, and the
// build that includes a file the inherited one left out.
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

// extract adds the declarations in pkgs to x. A package's files appear again in
// its test variant, so each file is read once, and the files its build context
// left out are remembered for readBuilds.
func (x *extractor) extract(pkgs []*packages.Package) {
	for _, p := range pkgs {
		if strings.HasSuffix(p.PkgPath, ".test") {
			continue // the generated test main
		}
		for _, name := range p.IgnoredFiles {
			if strings.HasSuffix(name, ".go") {
				x.ignored = append(x.ignored, name)
			}
		}
		for _, f := range p.Syntax {
			name := p.Fset.File(f.Pos()).Name()
			if x.seen[name] {
				continue
			}
			x.seen[name] = true
			x.file(p, f, name)
		}
	}
}

type extractor struct {
	dir     string
	seen    map[string]bool
	ignored []string
	// loaded holds, per build and package directory, the errors that build's
	// load reported -- nil when it read the package. A sibling file needing the
	// same build costs no second load, and still learns why the build failed.
	loaded  map[string][]string
	rules   []Rule
	ruleAt  map[string]string
	tables  []Table
	codecs  map[string]*Codec
	codecAt map[string]string
	upcasts []upcast
	errs    []string
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

func (x *extractor) file(p *packages.Package, f *ast.File, name string) {
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
	// Codecs registered only in tests never write to the ledger.
	if strings.HasSuffix(name, "_test.go") {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			x.codecCall(p, call)
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

func (x *extractor) codecCall(p *packages.Package, call *ast.CallExpr) {
	info := p.TypesInfo
	switch {
	case isFunc(info, call, codecPath, "Register"):
		inst, ok := info.Instances[callee(call)]
		if !ok || inst.TypeArgs == nil || inst.TypeArgs.Len() < 2 {
			// A registration the type checker could not instantiate has no
			// entry in Instances, and its type arguments are the only place
			// the stored type and the codec are written. There is nothing to
			// record, so this is an error rather than a zero value.
			x.errorf(p.Fset, call.Pos(), "codec.Register: the type checker could not instantiate this call, so the type an entry is stored as cannot be read")
			return
		}
		v, sum := inst.TypeArgs.At(0), inst.TypeArgs.At(1)
		subject := "codec.Register[" + types.TypeString(types.Unalias(v), nil) + "]"
		if !x.concrete(p, call.Pos(), v, subject, "the stored shape comes from the caller's type argument") {
			return
		}
		if !x.concrete(p, call.Pos(), sum, subject, "the codec a stored entry names comes from the caller's type argument") {
			return
		}
		name, okName := x.constant(p, call.Args[1])
		version, okVersion := x.constant(p, call.Args[2])
		if !okName || !okVersion {
			x.errorf(p.Fset, call.Pos(), "codec.Register[%s]: the name and version must be constants", types.TypeString(v, nil))
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
		at := x.position(p.Fset, call.Pos())
		if prev, dup := x.codecAt[c.key()]; dup {
			x.errorf(p.Fset, call.Pos(), "%s is also registered at %s", c.Name, prev)
			return
		}
		x.codecAt[c.key()] = at
		x.codecs[c.key()] = c
	case isMethod(info, call, codecPath, "Codec", "Upcast"):
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
		x.errorf(p.Fset, call.Pos(), "Upcast: %s declares its own Upcast, so the codec it registers on cannot be read; call Upcast on the codec itself", types.TypeString(info.Selections[call.Fun.(*ast.SelectorExpr)].Recv(), nil))
	}
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
