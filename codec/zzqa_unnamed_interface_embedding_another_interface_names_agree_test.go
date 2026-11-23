package codec_test

// An unnamed interface is a supported codec sum, and one written as an embedded
// interface is still unnamed: `interface{ fmt.Stringer }`. The static extractor
// records it with go/types, which keeps the embedded interface's name, while
// the runtime half of the cross-check builds the same interface from reflect,
// which exposes only its flattened method set. So one correct registration is
// keyed under two different sums and codec.CheckRegistrations reports it as
// both missing from the manifest and extra in the codecs.
//
// This is t2800's class — the runtime and static halves name an unnamed
// interface differently — in the case where the interface embeds another
// interface rather than declaring methods directly. The test reads the actual
// manifest spelling from the tool rather than pinning one side, so it passes
// whichever half a fix normalizes.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// zzqaEmbeddedStringer is a value of the unnamed sum below: it implements
// fmt.Stringer.
type zzqaEmbeddedStringer struct {
	S string `json:"s"`
}

func (zzqaEmbeddedStringer) String() string { return "embedded" }

func TestAnUnnamedInterfaceSumEmbeddingAnotherInterfaceNamesTheSameWayAsTheManifest(t *testing.T) {
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
		"module example.com/zzqaembedded", 1)
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
	probe := `package zzqaembedded

import (
	"fmt"

	"github.com/anyweez/cairn/codec"
)

type Event struct {
	S string ` + "`json:\"s\"`" + `
}

func (Event) String() string { return "embedded" }

var C = func() *codec.Codec[interface{ fmt.Stringer }] {
	c := codec.New[interface{ fmt.Stringer }]()
	codec.Register[Event, interface{ fmt.Stringer }](c, "zzqa.embedded", 1)
	return c
}()
`
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "-C", filepath.Join(root, "cmd", "cairn-manifest"),
		"run", ".", "-dir", dir, "-o", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generating the manifest: %v\n%s", err, out)
	}
	manifestSum := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "- sum: "); ok {
			manifestSum = rest
			break
		}
	}
	if manifestSum == "" {
		t.Fatalf("the manifest names no sum in:\n%s", out)
	}

	c := codec.New[interface{ fmt.Stringer }]()
	codec.Register[zzqaEmbeddedStringer, interface{ fmt.Stringer }](c, "zzqa.embedded", 1)
	registrations := c.Registrations()
	if len(registrations) != 1 {
		t.Fatalf("registrations: %+v", registrations)
	}
	if got := registrations[0].Sum; got != manifestSum {
		t.Errorf("the codecs name the sum %q, the manifest records %q", got, manifestSum)
	}
	if problems := codec.CheckRegistrations(
		[]codec.Registration{{Sum: manifestSum, Name: "zzqa.embedded", Version: 1}},
		registrations,
	); len(problems) > 0 {
		t.Errorf("the documented cross-check reports one correct codec as two:\n  %s",
			strings.Join(problems, "\n  "))
	}
}
