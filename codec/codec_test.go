package codec_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/anyweez/cairn/codec"
)

type shape interface{ area() int }

type circle struct{}

func (circle) area() int { return 0 }

// Version 2 of square renamed Side to Edge.
type square struct {
	Edge int `json:"edge"`
}

func (s square) area() int { return s.Edge * s.Edge }

func TestOldVersionsAreUpcastBeforeDecoding(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[square](c, "square", 2)
	c.Upcast("square", 1, func(old json.RawMessage) (json.RawMessage, error) {
		var v1 struct {
			Side int `json:"side"`
		}
		if err := json.Unmarshal(old, &v1); err != nil {
			return nil, err
		}
		return json.Marshal(square{Edge: v1.Side})
	})

	got, err := c.Decode(codec.Envelope{Type: "square", Version: 1, Data: json.RawMessage(`{"side":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got.area() != 9 {
		t.Fatalf("decoded %+v", got)
	}

	env, err := c.Encode(square{Edge: 2})
	if err != nil || env.Version != 2 || env.Type != "square" {
		t.Fatalf("encoded %+v, %v", env, err)
	}

	latest, err := c.Latest(codec.Envelope{Type: "square", Version: 1, Data: json.RawMessage(`{"side":3}`)})
	if err != nil || string(latest) != `{"edge":3}` {
		t.Fatalf("latest %s, %v", latest, err)
	}
	if _, err := c.Latest(codec.Envelope{Type: "square", Version: 3, Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("a version newer than the build has no latest form")
	}
}

func TestDecodingRefusesWhatItCannotRead(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[square](c, "square", 2)

	for name, env := range map[string]codec.Envelope{
		"unknown type":     {Type: "circle", Version: 1, Data: json.RawMessage(`{}`)},
		"newer version":    {Type: "square", Version: 3, Data: json.RawMessage(`{"edge":1}`)},
		"missing upcaster": {Type: "square", Version: 1, Data: json.RawMessage(`{"side":1}`)},
		// A field the type no longer has means its shape changed without a
		// version bump; dropping it silently would lose data.
		"unknown field": {Type: "square", Version: 2, Data: json.RawMessage(`{"edge":1,"colour":"red"}`)},
	} {
		if _, err := c.Decode(env); err == nil {
			t.Errorf("%s: decoded without error", name)
		}
	}
}

// A Marshaler decides its own encoding, so the walk must not read its fields.
type marshalsItself struct{ Raw string }

func (marshalsItself) MarshalJSON() ([]byte, error) { return []byte(`"ok"`), nil }

func TestCheckStorableRejectsTextTheLedgerCannotKeep(t *testing.T) {
	type inner struct {
		Title string         `json:"title"`
		Tags  map[string]any `json:"tags"`
	}

	if err := codec.CheckStorable(inner{Title: "ship it", Tags: map[string]any{"note": "fine", "rune": "\ufffd"}}); err != nil {
		t.Fatalf("valid text was rejected: %v", err)
	}
	if err := codec.CheckStorable(marshalsItself{Raw: "ship\xffit"}); err != nil {
		t.Fatalf("a Marshaler's own encoding should be trusted: %v", err)
	}

	for name, v := range map[string]any{
		"invalid utf8":        inner{Title: "ship \xff it"},
		"nul":                 inner{Title: "ship\x00it"},
		"invalid utf8 in map": inner{Title: "ok", Tags: map[string]any{"note": "v\xff"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := codec.CheckStorable(v); err == nil {
				t.Fatalf("CheckStorable accepted %#v", v)
			}
		})
	}
}

func TestRegisterRejectsTypesOutsideTheSum(t *testing.T) {
	defer func() {
		if p := recover(); p == nil || !strings.Contains(p.(string), "does not implement") {
			t.Fatalf("expected a panic, got %v", p)
		}
	}()
	codec.Register[struct{ X int }](codec.New[shape](), "nope", 1)
}

func TestRegistrationsDescribeTheCodec(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[square](c, "square", 2)
	c.Upcast("square", 1, func(old json.RawMessage) (json.RawMessage, error) { return old, nil })
	codec.Register[circle](c, "circle", 1)

	got := c.Registrations()
	want := []codec.Registration{
		{Name: "circle", Version: 1},
		{Name: "square", Version: 2, Upcasts: []int{1}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d registrations, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Version != want[i].Version || !slices.Equal(got[i].Upcasts, want[i].Upcasts) {
			t.Errorf("registration %d: got %+v, want %+v", i, got[i], want[i])
		}
		if got[i].Sum != got[0].Sum || !strings.HasSuffix(got[i].Sum, ".shape") {
			t.Errorf("registration %d: sum is %q, want the sum type's full Go path", i, got[i].Sum)
		}
	}
}

// The runtime half of the manifest cross-check: the two ways static extraction
// misreads codec setup are each a difference here.
func TestCheckRegistrations(t *testing.T) {
	base := []codec.Registration{{Sum: "example.com/x.Event", Name: "x.happened", Version: 2, Upcasts: []int{1}}}
	for _, tc := range []struct {
		name      string
		want, got []codec.Registration
		problem   string
	}{
		{"agree", base, base, ""},
		{"an upcaster that never runs", base, []codec.Registration{{Sum: "example.com/x.Event", Name: "x.happened", Version: 2}}, "upcaster from version 1 in the manifest but not in the codecs"},
		{"an upcaster the tool missed", []codec.Registration{{Sum: "example.com/x.Event", Name: "x.happened", Version: 2}}, base, "upcaster from version 1 in the codecs but not in the manifest"},
		{"indirect registration", nil, base, "is registered by the codecs but not in the manifest"},
		{"a registration the tool missed", base, nil, "is in the manifest but not registered by the codecs"},
		{"a version the tool misread", base, []codec.Registration{{Sum: "example.com/x.Event", Name: "x.happened", Version: 3, Upcasts: []int{1}}}, "version 2 in the manifest but version 3 in the codecs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(codec.CheckRegistrations(tc.want, tc.got), "\n")
			if tc.problem == "" {
				if got != "" {
					t.Fatalf("got %q, want no problems", got)
				}
				return
			}
			if !strings.Contains(got, tc.problem) {
				t.Fatalf("got %q, want a problem containing %q", got, tc.problem)
			}
		})
	}
}

// A defined type is named by go/types even when its underlying kind is a
// composite: `type Listing []string` is example.com/x.Listing, not []string.
// A registration rendered by reflect as the underlying type would name a
// different sum than the manifest and fail the cross-check twice for one
// correct codec.
type namedListing []string
type namedLookup map[string]int
type namedPair [2]string
type namedCount *int
type namedList[T any] []T

func sumNameOf[T any](t *testing.T) string {
	t.Helper()
	c := codec.New[T]()
	codec.Register[T](c, "test.value", 1)
	rs := c.Registrations()
	if len(rs) != 1 {
		t.Fatalf("got %d registrations, want 1", len(rs))
	}
	return rs[0].Sum
}

func TestRegistrationsNameADefinedCompositeAsGoTypesDoes(t *testing.T) {
	const pkg = "github.com/anyweez/cairn/codec_test"
	for _, tc := range []struct {
		name string
		sum  func(*testing.T) string
		want string
	}{
		{"a defined slice", sumNameOf[namedListing], pkg + ".namedListing"},
		{"a defined map", sumNameOf[namedLookup], pkg + ".namedLookup"},
		{"a defined array", sumNameOf[namedPair], pkg + ".namedPair"},
		{"a defined pointer", sumNameOf[namedCount], pkg + ".namedCount"},
		{"a defined generic slice", sumNameOf[namedList[string]], pkg + ".namedList[string]"},
		{"an unnamed slice", sumNameOf[[]string], "[]string"},
		{"a pointer to a defined slice", sumNameOf[*namedListing], "*" + pkg + ".namedListing"},
		{"a slice of defined slices", sumNameOf[[]namedListing], "[]" + pkg + ".namedListing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sum(t); got != tc.want {
				t.Errorf("sum: got %q, want %q", got, tc.want)
			}
		})
	}
}
