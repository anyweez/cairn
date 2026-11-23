package codec_test

// The runtime and the static extractor are two halves of one check:
// codec.CheckRegistrations accepts a manifest a program generates only if both
// name a codec's sum type the same way. The class of defects behind that check
// has been found one spelling at a time — a defined composite expanded into its
// underlying type, `any` written `interface {}`, generic type arguments joined
// with "," instead of ", ", method parameter names reflect cannot see, an
// embedded interface kept as an embed — so this test compares the two halves
// across every shape the class has fixed, against the real extractor rather
// than a copy of its rules. The shapes reflect cannot recover are pinned below
// as known divergences, so a new one shows up here instead of in a user's
// cross-check.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/anyweez/cairn/codec"
)

const (
	parityHostPkg  = "github.com/anyweez/cairn/codec_test"
	parityProbePkg = "example.com/zzqaparity"
)

// parityProbeSource registers one codec per sum shape. The declared types
// mirror the codec_test ones the runtime half below instantiates, so the only
// difference between the two renderings is the module path, which the test
// normalizes.
const parityProbeSource = `package zzqaparity

import (
	"fmt"

	"github.com/anyweez/cairn/codec"
)

type Value struct{ S string }

func (Value) M() int                                           { return 0 }
func (Value) N(string)                                          {}
func (Value) String() string                                    { return "" }
func (Value) Accept(x int, done func(n int) error) (err error)  { return nil }
func (Value) isPairSum()                                        {}
func (Value) isTripleSum()                                      {}

type namedListing []string
type namedLookup map[string]int
type namedPair [2]string
type namedCount *int
type namedList[T any] []T

type pairSum[A, B any] interface{ isPairSum() }
type tripleSum[A, B, C any] interface{ isTripleSum() }
type twoArgSlice[A, B any] []A

var (
	definedSlice     = codec.New[namedListing]()
	definedMap       = codec.New[namedLookup]()
	definedArray     = codec.New[namedPair]()
	definedPointer   = codec.New[namedCount]()
	definedGeneric   = codec.New[namedList[string]]()
	twoArguments     = codec.New[pairSum[namedListing, string]]()
	threeArguments   = codec.New[tripleSum[namedListing, string, int]]()
	nestedArguments  = codec.New[pairSum[pairSum[namedListing, int], string]]()
	genericComposite  = codec.New[twoArgSlice[namedListing, string]]()
	interfaceMethod  = codec.New[interface{ M() int }]()
	interfaceNames   = codec.New[interface{ Accept(x int, done func(n int) error) (err error) }]()
	interfaceEmbed   = codec.New[interface{ fmt.Stringer }]()
	emptyAny         = codec.New[any]()
	structSum        = codec.New[struct{ N int; S string }]()
	sliceSum         = codec.New[[]string]()
	mapSum           = codec.New[map[string]int]()
	arraySum         = codec.New[[2]int]()
	pointerSum       = codec.New[*int]()
	sendChan         = codec.New[chan<- int]()
	recvChan         = codec.New[<-chan int]()
	funcSum          = codec.New[func(int, ...string) (int, error)]()
	funcNamedParams  = codec.New[func(a int) (b string)]()
	sliceOfDefined   = codec.New[[]namedListing]()
	mapOfInterface   = codec.New[map[string]interface{ M() int }]()

	emptyInterface   = codec.New[interface{}]()
	structArgument   = codec.New[pairSum[struct{ X int }, string]]()
	ifaceArgument    = codec.New[pairSum[interface{}, string]]()
)

func init() {
	codec.Register[namedListing, namedListing](definedSlice, "parity.defined-slice", 1)
	codec.Register[namedLookup, namedLookup](definedMap, "parity.defined-map", 1)
	codec.Register[namedPair, namedPair](definedArray, "parity.defined-array", 1)
	codec.Register[namedCount, namedCount](definedPointer, "parity.defined-pointer", 1)
	codec.Register[namedList[string], namedList[string]](definedGeneric, "parity.defined-generic-slice", 1)
	codec.Register[Value, pairSum[namedListing, string]](twoArguments, "parity.two-arguments", 1)
	codec.Register[Value, tripleSum[namedListing, string, int]](threeArguments, "parity.three-arguments", 1)
	codec.Register[Value, pairSum[pairSum[namedListing, int], string]](nestedArguments, "parity.nested-arguments", 1)
	codec.Register[twoArgSlice[namedListing, string], twoArgSlice[namedListing, string]](genericComposite, "parity.defined-generic-composite", 1)
	codec.Register[Value, interface{ M() int }](interfaceMethod, "parity.iface-method", 1)
	codec.Register[Value, interface{ Accept(x int, done func(n int) error) (err error) }](interfaceNames, "parity.iface-named-params", 1)
	codec.Register[Value, interface{ fmt.Stringer }](interfaceEmbed, "parity.iface-embedded", 1)
	codec.Register[Value, any](emptyAny, "parity.any", 1)
	codec.Register[struct{ N int; S string }, struct{ N int; S string }](structSum, "parity.struct", 1)
	codec.Register[[]string, []string](sliceSum, "parity.slice", 1)
	codec.Register[map[string]int, map[string]int](mapSum, "parity.map", 1)
	codec.Register[[2]int, [2]int](arraySum, "parity.array", 1)
	codec.Register[*int, *int](pointerSum, "parity.pointer", 1)
	codec.Register[chan<- int, chan<- int](sendChan, "parity.send-chan", 1)
	codec.Register[<-chan int, <-chan int](recvChan, "parity.recv-chan", 1)
	codec.Register[func(int, ...string) (int, error), func(int, ...string) (int, error)](funcSum, "parity.func", 1)
	codec.Register[func(a int) (b string), func(a int) (b string)](funcNamedParams, "parity.func-named-params", 1)
	codec.Register[[]namedListing, []namedListing](sliceOfDefined, "parity.slice-of-defined", 1)
	codec.Register[map[string]interface{ M() int }, map[string]interface{ M() int }](mapOfInterface, "parity.map-of-interface", 1)

	codec.Register[Value, interface{}](emptyInterface, "parity.empty-interface", 1)
	codec.Register[Value, pairSum[struct{ X int }, string]](structArgument, "parity.struct-argument", 1)
	codec.Register[Value, pairSum[interface{}, string]](ifaceArgument, "parity.empty-interface-argument", 1)
}
`

type parityStringer struct{}

func (parityStringer) String() string { return "parity" }

func TestTheManifestAndTheRuntimeNameEverySumTheSameWay(t *testing.T) {
	manifest := manifestSums(t)

	parity := []struct {
		name string
		sum  func(*testing.T) string
	}{
		{"parity.defined-slice", sumName[namedListing, namedListing]},
		{"parity.defined-map", sumName[namedLookup, namedLookup]},
		{"parity.defined-array", sumName[namedPair, namedPair]},
		{"parity.defined-pointer", sumName[namedCount, namedCount]},
		{"parity.defined-generic-slice", sumName[namedList[string], namedList[string]]},
		{"parity.two-arguments", sumName[pairItem, pairSum[namedListing, string]]},
		{"parity.three-arguments", sumName[pairItem, tripleSum[namedListing, string, int]]},
		{"parity.nested-arguments", sumName[pairItem, pairSum[pairSum[namedListing, int], string]]},
		{"parity.defined-generic-composite", sumName[twoArgSlice[namedListing, string], twoArgSlice[namedListing, string]]},
		{"parity.iface-method", sumName[hasM, interface{ M() int }]},
		{"parity.iface-named-params", sumName[namedParams, interface {
			Accept(x int, done func(n int) error) (err error)
		}]},
		{"parity.iface-embedded", sumName[parityStringer, interface{ fmt.Stringer }]},
		{"parity.any", sumName[hasM, any]},
		{"parity.struct", sumName[struct {
			N int
			S string
		}, struct {
			N int
			S string
		}]},
		{"parity.slice", sumName[[]string, []string]},
		{"parity.map", sumName[map[string]int, map[string]int]},
		{"parity.array", sumName[[2]int, [2]int]},
		{"parity.pointer", sumName[*int, *int]},
		{"parity.send-chan", sumName[chan<- int, chan<- int]},
		{"parity.recv-chan", sumName[<-chan int, <-chan int]},
		{"parity.func", sumName[func(int, ...string) (int, error), func(int, ...string) (int, error)]},
		{"parity.func-named-params", sumName[func(a int) (b string), func(a int) (b string)]},
		{"parity.slice-of-defined", sumName[[]namedListing, []namedListing]},
		{"parity.map-of-interface", sumName[map[string]interface{ M() int }, map[string]interface{ M() int }]},
	}

	var want, got []codec.Registration
	for _, tc := range parity {
		fromManifest, ok := manifest[tc.name]
		if !ok {
			t.Fatalf("the manifest records no codec named %q", tc.name)
		}
		fromManifest = strings.ReplaceAll(fromManifest, parityProbePkg, parityHostPkg)
		runtimeSum := tc.sum(t)
		if runtimeSum != fromManifest {
			t.Errorf("%s: the codecs name the sum %q, the manifest records %q", tc.name, runtimeSum, fromManifest)
		}
		want = append(want, codec.Registration{Sum: fromManifest, Name: tc.name, Version: 1})
		got = append(got, codec.Registration{Sum: runtimeSum, Name: tc.name, Version: 1})
	}
	if problems := codec.CheckRegistrations(want, got); len(problems) > 0 {
		t.Errorf("the documented cross-check rejects correctly registered codecs:\n  %s", strings.Join(problems, "\n  "))
	}

	// The shapes reflect cannot recover. They are expected to differ, and
	// pinning both spellings means a change to either half shows up here rather
	// than in a user's manifest check.
	divergences := []struct {
		name     string
		sum      func(*testing.T) string
		runtime  string
		manifest string
	}{
		{
			name:     "parity.empty-interface",
			sum:      sumName[hasM, interface{}],
			runtime:  "any",
			manifest: "interface{}",
		},
		{
			name:     "parity.struct-argument",
			sum:      sumName[pairItem, pairSum[struct{ X int }, string]],
			runtime:  parityHostPkg + ".pairSum[struct { X int }, string]",
			manifest: parityProbePkg + ".pairSum[struct{X int}, string]",
		},
		{
			name:     "parity.empty-interface-argument",
			sum:      sumName[pairItem, pairSum[interface{}, string]],
			runtime:  parityHostPkg + ".pairSum[interface {}, string]",
			manifest: parityProbePkg + ".pairSum[interface{}, string]",
		},
	}
	for _, tc := range divergences {
		fromManifest, ok := manifest[tc.name]
		if !ok {
			t.Fatalf("the manifest records no codec named %q", tc.name)
		}
		runtimeSum := tc.sum(t)
		if runtimeSum != tc.runtime {
			t.Errorf("%s: the codecs now name the sum %q, pinned known divergence was %q", tc.name, runtimeSum, tc.runtime)
		}
		if fromManifest != tc.manifest {
			t.Errorf("%s: the manifest now records %q, pinned known divergence was %q", tc.name, fromManifest, tc.manifest)
		}
		if runtimeSum == fromManifest {
			t.Errorf("%s: the two halves agree on %q, so this is no longer a known divergence", tc.name, runtimeSum)
		}
	}

	if got, want := len(manifest), len(parity)+len(divergences); got != want {
		t.Errorf("the manifest records %d codecs, the probe registers %d", got, want)
	}
}

// manifestSums writes the probe module, runs the real manifest tool over it, and
// returns the sum the tool records for each codec name.
func manifestSums(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	rootMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	gomod := strings.Replace(string(rootMod), "module github.com/anyweez/cairn",
		"module "+parityProbePkg, 1)
	gomod += "\nrequire github.com/anyweez/cairn v0.0.0\n\n" +
		"replace github.com/anyweez/cairn => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	gosum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), gosum, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(parityProbeSource), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "-C", filepath.Join(root, "cmd", "cairn-manifest"),
		"run", ".", "-dir", dir, "-o", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generating the manifest: %v\n%s", err, out)
	}
	var doc struct {
		Codecs []struct {
			Sum  string `yaml:"sum"`
			Name string `yaml:"name"`
		} `yaml:"codecs"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("reading the manifest: %v\n%s", err, out)
	}
	sums := make(map[string]string, len(doc.Codecs))
	for _, c := range doc.Codecs {
		sums[c.Name] = c.Sum
	}
	return sums
}
