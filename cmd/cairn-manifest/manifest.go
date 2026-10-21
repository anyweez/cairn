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
// aren't constants, or a rule ID declared twice.
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
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
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

	x := &extractor{
		dir: dir, seen: map[string]bool{},
		ruleAt: map[string]string{}, codecs: map[string]*Codec{}, codecAt: map[string]string{},
	}
	for _, p := range pkgs {
		if strings.HasSuffix(p.PkgPath, ".test") {
			continue // the generated test main
		}
		for _, f := range p.Syntax {
			// A package's files appear again in its test variant.
			name := p.Fset.File(f.Pos()).Name()
			if x.seen[name] {
				continue
			}
			x.seen[name] = true
			x.file(p, f, name)
		}
	}
	x.upcastsToCodecs()
	if len(x.errs) > 0 {
		slices.Sort(x.errs)
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

type upcast struct {
	sum, name string
	from      int
	pos       string
}

type extractor struct {
	dir     string
	seen    map[string]bool
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
	if rel, err := filepath.Rel(x.dir, p.Filename); err == nil {
		p.Filename = rel
	}
	return fmt.Sprintf("%s:%d", p.Filename, p.Line)
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
		inst := info.Instances[callee(call)]
		v, sum := inst.TypeArgs.At(0), inst.TypeArgs.At(1)
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
		// The codec can be named through an alias of *codec.Codec[T], which
		// go/types keeps as an *types.Alias until it is resolved; unalias before
		// and after the pointer test so the receiver is *codec.Codec[T].
		recv := types.Unalias(info.Selections[call.Fun.(*ast.SelectorExpr)].Recv())
		if ptr, ok := recv.(*types.Pointer); ok {
			recv = types.Unalias(ptr.Elem())
		}
		sum := recv.(*types.Named).TypeArgs().At(0)
		name, okName := x.constant(p, call.Args[0])
		from, okFrom := x.constant(p, call.Args[1])
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

func isMethod(info *types.Info, call *ast.CallExpr, pkg, recv, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	s, ok := info.Selections[sel]
	if !ok || s.Kind() != types.MethodVal {
		return false
	}
	fn := s.Obj().(*types.Func)
	// The receiver can be named through an alias of *codec.Codec[T], which
	// go/types keeps as an *types.Alias; unalias before and after the pointer
	// test, or the pointer never matches and isNamed sees the alias.
	r := types.Unalias(s.Recv())
	if ptr, ok := r.(*types.Pointer); ok {
		r = types.Unalias(ptr.Elem())
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == pkg && fn.Name() == name && isNamed(r, pkg, recv)
}
