package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExtractReadsEverything(t *testing.T) {
	m, err := Extract("testdata/good", "./...")
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, r := range m.Rules {
		ids = append(ids, r.ID+" "+r.Kind+" "+r.Status)
	}
	want := []string{"G1 can proposed", "G2 never established", "G3 never established"}
	if !slices.Equal(ids, want) {
		t.Errorf("rules: got %v, want %v (package-level only, test files included)", ids, want)
	}

	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: got %+v, want only good.published (test registrations are fixtures)", m.Codecs)
	}
	c := m.Codecs[0]
	if c.Name != "good.published" || c.Version != 3 || c.Sum != "example.com/good.Event" || !slices.Equal(c.Upcasts, []int{1, 2}) {
		t.Errorf("codec: %+v", c)
	}
	wantFields := []string{
		"by: string",                             // embedded struct, flattened
		"title: string",                          //
		"body: bytes",                            // []byte is base64
		"tags?: map[string]int",                  // omitempty
		"at: time.Time",                          // marshals itself
		"raw: encoding/json.RawMessage",          //
		"chain: {next?: *example.com/good.Node}", // recursion stops at the name
		"status: string",                         // a named string is a string
		"-: string",                              // json:"-," names the field "-"
		"Untagged: int",                          //
		"extra?: map[string]string",              // omitzero
		"count: string(int)",                     // ,string writes the number inside a string
		"labels: []string",                       // ,string applies only to scalars
		"inline: {a: int}",                       // not addressable: the pointer marshaler is unused
		"behind: *example.com/good.PtrMarshal",   // behind a pointer it marshals itself
		"many: []example.com/good.PtrMarshal",    // slice elements are addressable
		"nested: {n: int}",                       // embedded unexported struct, tagged, so marshaled as a named object
	}
	if !slices.Equal(c.Fields, wantFields) {
		t.Errorf("fields:\n got %q\nwant %q", c.Fields, wantFields)
	}

	if len(m.Tables) != 1 || m.Tables[0].Name != "example.com/good.Lifecycle" {
		t.Fatalf("tables: %+v", m.Tables)
	}
	edges := m.Tables[0].Edges
	if len(edges) != 2 || edges[0] != (Edge{From: "draft", On: "publish", To: "published"}) {
		t.Errorf("edges are sorted and use constant values: %+v", edges)
	}
}

// A type that embeds a pointer to itself is legal Go and encoding/json
// marshals it, but the shape walk recurses through the embedded field without
// the guard shape() keeps for named fields, so the tool loops until the stack
// overflows.
func TestExtractHandlesRecursiveEmbeddedPointer(t *testing.T) {
	m, err := Extract("testdata/recurse", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	// encoding/json visits the type once and skips the recursive embedded
	// pointer, so the stored shape is just v.
	if got, want := m.Codecs[0].Fields, []string{"v: int"}; !slices.Equal(got, want) {
		t.Errorf("fields: got %q, want %q", got, want)
	}
}

// A named field that points back to its own type is not an embedded-flatten
// recursion: encoding/json renders the field once and ends the type at its
// name. The embedded-walk guard must leave this rendering alone.
func TestExtractKeepsNamedSelfReference(t *testing.T) {
	m, err := Extract("testdata/namedrecurse", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	want := []string{"v: int", "next: *{v: int, next: *example.com/namedrecurse.T}"}
	if got := m.Codecs[0].Fields; !slices.Equal(got, want) {
		t.Errorf("fields: got %q, want %q", got, want)
	}
}

// Go models `type A = T` as a *types.Alias, which is not a *types.Named, so a
// self-reference through an alias used to slip past the recursion guard and
// overflow the stack. The alias denotes T, so the shape is T's.
func TestExtractHandlesAliasedSelfReference(t *testing.T) {
	m, err := Extract("testdata/aliasrecurse", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	want := []string{"v: int", "next: *{v: int, next: *example.com/aliasrecurse.Node}"}
	if got := m.Codecs[0].Fields; !slices.Equal(got, want) {
		t.Errorf("fields: got %q, want %q", got, want)
	}
}

// The same alias in an untagged embedded field: `type Node struct { *A; V
// int }` with `A = Node`. encoding/json flattens the embedded pointer once and
// skips it thereafter, so the shape is just v.
func TestExtractHandlesEmbeddedAlias(t *testing.T) {
	m, err := Extract("testdata/embedalias", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	if got, want := m.Codecs[0].Fields, []string{"v: int"}; !slices.Equal(got, want) {
		t.Errorf("fields: got %q, want %q", got, want)
	}
}

// A self-embedding type reached as an ordinary field flattens its embedded
// pointer once, not once per enclosing struct. encoding/json writes the field
// as {"v":...}, so the shape is {v: int}, not {v: int, v: int}.
func TestExtractHandlesNestedSelfEmbedding(t *testing.T) {
	m, err := Extract("testdata/nestedembed", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	if got, want := m.Codecs[0].Fields, []string{"n: {v: int}"}; !slices.Equal(got, want) {
		t.Errorf("fields: got %q, want %q", got, want)
	}

	// The reported shape is the one encoding/json writes for the same type.
	type node struct {
		*node
		V int `json:"v"`
	}
	type outer struct {
		N node `json:"n"`
	}
	got, err := json.Marshal(outer{N: node{V: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"n":{"v":1}}`; string(got) != want {
		t.Errorf("encoding/json: got %s, want %s", got, want)
	}
}

func TestExtractRefusesToGuess(t *testing.T) {
	_, err := Extract("testdata/bad", "./...")
	if err == nil {
		t.Fatal("non-constant declarations must be refused")
	}
	for _, want := range []string{
		"bad.go:14: rule Dynamic: Statement is not a constant",
		"bad.go:18: rule ID B2 is also declared at bad.go:16",
		"bad.go:20: rule Copied must be a spec.Rule literal",
		"bad.go:24: table Spread must list its edges, not pass a slice",
		"bad.go:34: codec.Register[example.com/bad.Ping]: the name and version must be constants",
		"bad.go:35: upcaster for missing, which is not registered in example.com/bad.Msg",
		"bad.go:38: rule Positional must name its fields",
		"bad.go:42: table Indirect must be built by a direct fsm.New call",
		"bad.go:46: table NonConstEdge: To is not a constant",
		"bad.go:51: pong is also registered at bad.go:50",
		"bad.go:52: Upcast: the name and version must be constants",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if n := strings.Count(err.Error(), "\n") + 1; n != 11 {
		t.Errorf("got %d errors, want 11:\n%v", n, err)
	}
}

// A registration reached through a generic helper writes the helper's type
// parameter, whose JSON shape is its constraint ("=: any"), so every
// instantiation of the helper looked alike and -base missed a wire change to the
// type the caller supplies. The extractor states what is stored or refuses, so
// the parameter must be an error rather than a recorded shape.
func TestExtractRefusesGenericRegistrations(t *testing.T) {
	_, err := Extract("testdata/genericregister", "./...")
	if err == nil {
		t.Fatal("a registration through a type parameter must be refused, not recorded as its constraint")
	}
	for _, want := range []string{
		"genericregister.go:18: codec.Register[V]: V is a type parameter, so the stored shape comes from the caller's type argument, not V",
		"genericregister.go:22: codec.Register[example.com/genericregister.Event]: T is a type parameter, so the codec a stored entry names comes from the caller's type argument, not T",
		"genericregister.go:26: Upcast: T is a type parameter, so the codec this upcaster belongs to comes from the caller's type argument, not T",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if n := strings.Count(err.Error(), "\n") + 1; n != 3 {
		t.Errorf("got %d errors, want 3:\n%v", n, err)
	}
}

// Generics that do not choose what is stored are ordinary code: the module must
// load, and a concrete registration records its shape as before.
func TestExtractReadsAModuleThatUsesGenericsElsewhere(t *testing.T) {
	m, err := Extract("testdata/genericok", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	c := m.Codecs[0]
	if c.Sum != "example.com/genericok.Msg" || c.Type != "example.com/genericok.Event" || !slices.Equal(c.Fields, []string{"v: int"}) {
		t.Errorf("codec: %+v", c)
	}
}

// A module whose packages go list refuses to load. It is the state a module is
// in right after someone adds an import and before `go mod tidy`, and x/tools
// reports it as zero packages and a nil error -- which reads as a module that
// declares nothing, so the tool wrote an empty manifest and exited 0. The
// fixture declares a table and a rule, so an empty manifest says the load read
// nothing rather than that the module has nothing to say.
func TestExtractRefusesALoadThatReadNothing(t *testing.T) {
	_, err := Extract("testdata/unloadable", "./...")
	if err == nil {
		t.Fatal("a load that read no packages must be an error, not an empty manifest")
	}
	if !strings.Contains(err.Error(), "updates to go.mod needed") {
		t.Errorf("the loader's own message must be reported, got: %v", err)
	}

	// go list succeeds here and reports no packages, so there is no loader
	// message to pass on; the tool still must not call the module empty.
	_, err = Extract("testdata/nopackages", "./...")
	if err == nil || !strings.Contains(err.Error(), "no packages match ./...") {
		t.Errorf("a module with no packages must be an error, got: %v", err)
	}
}

// A rule whose literal names an alias of spec.Rule is legal Go. It must be
// read like any other rule rather than crashing the extractor on a type
// assertion that assumes the literal's type is a *types.Named.
func TestExtractReadsARuleWrittenWithAnAliasedLiteral(t *testing.T) {
	m, err := Extract("testdata/alias", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Rules) != 1 || m.Rules[0].ID != "A1" || m.Rules[0].Kind != "never" {
		t.Fatalf("rules: %+v", m.Rules)
	}
}

// The committed manifest is what cairn's own code says.
func TestCairnManifestIsCurrent(t *testing.T) {
	m, err := Extract("../..", "./...")
	if err != nil {
		t.Fatal(err)
	}
	got, err := encode(m)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("../..", "spec.manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("spec.manifest.yaml is stale: run go -C cmd/cairn-manifest run . -dir ../..")
	}
}

// The -base path, as the PR check runs it: write the manifest to stdout and
// fail on an incompatible change since the base.
func TestRunChecksTheBase(t *testing.T) {
	m, err := Extract("testdata/good", "./...")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	same, err := encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := run("testdata/good", "-", write("same.yaml", same), []string{"./..."}); err != nil {
		t.Errorf("an unchanged base must pass: %v", err)
	}

	m.Codecs[0].Fields = append(m.Codecs[0].Fields, "gone: int")
	older, err := encode(m)
	if err != nil {
		t.Fatal(err)
	}
	err = run("testdata/good", "-", write("older.yaml", older), []string{"./..."})
	if err == nil || !strings.Contains(err.Error(), "good.published changed shape without a version bump") {
		t.Errorf("a reshaped codec must fail against the base, got %v", err)
	}

	if err := run("testdata/good", "-", write("bad.yaml", []byte("codecs: [")), []string{"./..."}); err == nil {
		t.Error("an unreadable base must fail, not pass")
	}

	// A base branch from before the switch to YAML has the manifest as JSON.
	legacy := `{"rules": [], "tables": [], "codecs": [{"sum": "example.com/good.Event", "name": "good.published",
		"version": 3, "type": "example.com/good.Published_", "upcasts": [1, 2], "fields": ["gone: int"]}]}`
	err = run("testdata/good", "-", write("spec.manifest.json", []byte(legacy)), []string{"./..."})
	if err == nil || !strings.Contains(err.Error(), "good.published changed shape without a version bump") {
		t.Errorf("a JSON base must still be read and checked, got %v", err)
	}
}

// A base that passes is not a reason to skip the write: -o still regenerates
// the manifest it names. Here -o and -base are the same file, the natural
// local invocation, and a compatible base leaves the regenerated manifest in
// place.
func TestRunWritesTheManifestWhenTheBasePasses(t *testing.T) {
	dir := copyGoodFixture(t)
	m, err := Extract(dir, "./...")
	if err != nil {
		t.Fatal(err)
	}
	data, err := encode(m)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "spec.manifest.yaml")
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run(dir, "spec.manifest.yaml", manifest, []string{"./..."}); err != nil {
		t.Fatalf("an unchanged base must pass: %v", err)
	}
	wrote, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("a passing run must still write the manifest: %v", err)
	}
	if string(wrote) != string(data) {
		t.Error("the written manifest is not the one this run generated")
	}
}

// A platform's own file must not read as a removal: the base was written by a
// host that built a different set of files, and the tree is correct on both.
func TestRunChecksTheBaseAcrossPlatforms(t *testing.T) {
	dir := constrainedModule(t)

	t.Setenv("GOOS", "linux")
	if err := run(dir, "linux.yaml", "", []string{"./..."}); err != nil {
		t.Fatalf("writing the base on linux: %v", err)
	}

	t.Setenv("GOOS", "darwin")
	if err := run(dir, "darwin.yaml", filepath.Join(dir, "linux.yaml"), []string{"./..."}); err != nil {
		t.Fatalf("the manifest generated on darwin reports the base's stored types as broken: %v", err)
	}
}

func TestIncompatible(t *testing.T) {
	codec := func(version int, fields []string, upcasts ...int) Codec {
		return Codec{Sum: "x.Event", Name: "x.happened", Version: version, Fields: fields, Upcasts: upcasts}
	}
	v1 := []string{"a: string"}
	v2 := []string{"a: string", "b: int"}
	one := func(fields ...string) []Codec { return []Codec{codec(1, fields)} }
	for _, tc := range []struct {
		name     string
		old, now []Codec
		want     string
	}{
		{"unchanged", []Codec{codec(1, v1)}, []Codec{codec(1, v1)}, ""},
		{"added", nil, []Codec{codec(1, v1)}, ""},
		{"bumped with an upcaster", []Codec{codec(1, v1)}, []Codec{codec(2, v2, 1)}, ""},
		{"removed", []Codec{codec(1, v1)}, nil, "x.happened (x.Event) was removed"},
		{"went back", []Codec{codec(2, v2, 1)}, []Codec{codec(1, v1)}, "went back from version 2 to 1"},
		{"reshaped", []Codec{codec(1, v1)}, []Codec{codec(1, v2)}, "changed shape without a version bump"},
		{"no upcaster", []Codec{codec(1, v1)}, []Codec{codec(3, v2, 2)}, "no upcaster from version 1"},
		{"bumped and reshaped", []Codec{codec(1, v1)}, []Codec{codec(2, v2, 1)}, ""},
		{"fields reordered", []Codec{codec(1, []string{"a: string", "b: int"})}, []Codec{codec(1, []string{"b: int", "a: string"})}, ""},
		{"upcaster dropped", []Codec{codec(2, v2, 1)}, []Codec{codec(2, v2)}, "no upcaster from version 1"},
		{"older upcaster dropped on a bump", []Codec{codec(2, v2, 1)}, []Codec{codec(3, v2, 2)}, "no upcaster from version 1"},

		// A nested struct is one string in its parent's field, so its field
		// order must be ignored the way the outer order already is.
		{"nested fields reordered", one("o: {a: string, b: int}"), one("o: {b: int, a: string}"), ""},
		{"nested fields reordered behind a pointer", one("o: *{a: string, b: int}"), one("o: *{b: int, a: string}"), ""},
		{"nested fields reordered in a slice element", one("o: []{a: string, b: int}"), one("o: []{b: int, a: string}"), ""},
		{"nested fields reordered in a map value", one("o: map[string]{a: string, b: int}"), one("o: map[string]{b: int, a: string}"), ""},
		{"nested fields reordered inside an anonymous struct", one("o: []{i: {a: string, b: int}, j: int}"), one("o: []{i: {b: int, a: string}, j: int}"), ""},
		// The comma in a function signature does not separate fields.
		{"nested fields reordered beside a function signature", one("o: {f: func(int, string) error, a: string}"), one("o: {a: string, f: func(int, string) error}"), ""},

		// A real change below the top level is still a wire change.
		{"nested field renamed", one("o: {a: string, b: int}"), one("o: {a: string, c: int}"), "changed shape without a version bump"},
		{"nested field retyped", one("o: {a: string, b: int}"), one("o: {a: string, b: string}"), "changed shape without a version bump"},
		{"nested field added", one("o: {a: string}"), one("o: {a: string, b: int}"), "changed shape without a version bump"},
		{"nested field removed", one("o: {a: string, b: int}"), one("o: {a: string}"), "changed shape without a version bump"},
		{"nested omitempty added", one("o: {a: string}"), one("o: {a?: string}"), "changed shape without a version bump"},
		{"nested string option added", one("o: {a: int}"), one("o: {a: string(int)}"), "changed shape without a version bump"},
		{"nested reorder hides a rename", one("o: {a: string, b: int}"), one("o: {b: int, c: string}"), "changed shape without a version bump"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(Incompatible(&Manifest{Codecs: tc.old}, &Manifest{Codecs: tc.now}), "\n")
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
