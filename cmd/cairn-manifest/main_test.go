package main

import (
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
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if n := strings.Count(err.Error(), "\n") + 1; n != 6 {
		t.Errorf("got %d errors, want 6:\n%v", n, err)
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
	want, err := os.ReadFile(filepath.Join("../..", "spec.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("spec.manifest.json is stale: run go -C cmd/cairn-manifest run . -dir ../..")
	}
}

func TestIncompatible(t *testing.T) {
	codec := func(version int, fields []string, upcasts ...int) Codec {
		return Codec{Sum: "x.Event", Name: "x.happened", Version: version, Fields: fields, Upcasts: upcasts}
	}
	v1 := []string{"a: string"}
	v2 := []string{"a: string", "b: int"}
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(Incompatible(&Manifest{Codecs: tc.old}, &Manifest{Codecs: tc.now}), "\n")
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
