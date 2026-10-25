package main

// The manifest describes a module, not the host that generated it. Extract's
// doc says so ("the manifest is the whole module's spec and not the spec of the
// platform the tool happens to run on") and the README repeats it ("A tree
// therefore produces the same manifest on every host, so the committed file is
// current everywhere and -base never reads a type that one platform stores and
// another does not as removed").
//
// readBuilds closed the half of that promise about a declaration in a file the
// host's build context excludes. It does not close the half about the *shape* of
// a declaration that the host's build does include: when a stored type's field
// has a type that each platform declares differently, the shape is read from the
// host's build and committed as if it were the module's. One tree then produces
// two manifests that disagree about a stored type, and -base reports the other
// platform's shape as a version-bump violation on a tree that is correct on
// both.
//
// Either answer is acceptable, as with every other declaration the extractor
// cannot state exactly: the same manifest on every host, or an error naming the
// platform-specific declaration. What is not acceptable is picking the host's
// shape and exiting 0.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// qaStampModule writes a module whose stored event has a field of a type each
// platform declares differently, which is the ordinary Go way to give a field a
// platform-specific representation. The registration itself is unconstrained,
// so the codec is read on every host; only its shape comes from the build.
func qaStampModule(t *testing.T) string {
	t.Helper()
	return writeModule(t, fmt.Sprintf("module example.com/stamp\n\ngo 1.26.0\n\nrequire github.com/anyweez/cairn v0.0.0\n\nreplace github.com/anyweez/cairn => %s\n", mustRepoRoot(t)), map[string]string{
		"events.go": `package stamp

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Created struct {
	ID string ` + "`json:\"id\"`" + `
	At Stamp  ` + "`json:\"at\"`" + `
}

func (Created) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Created, Event](c, "stamp.created", 1)
	return c
}
`,
		"stamp_linux.go": `//go:build linux

package stamp

// Stamp is a number in the ledger on this platform.
type Stamp int64
`,
		"stamp_other.go": `//go:build !linux

package stamp

// Stamp is a string in the ledger on this platform.
type Stamp string
`,
	})
}

// qaNamesThePlatformDeclaration reports whether err names the platform-specific
// declaration the extractor could not state: the type, or either file that
// declares it differently. A refusal that says none of them leaves the reader
// with a manifest they cannot fix.
func qaNamesThePlatformDeclaration(err error) bool {
	msg := err.Error()
	for _, want := range []string{"Stamp", "stamp_linux.go", "stamp_other.go"} {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

func TestQAStoredShapeIsTheSameOnEveryHost(t *testing.T) {
	dir := qaStampModule(t)

	extract := func(goos string) (*Manifest, []byte, error) {
		t.Setenv("GOOS", goos)
		m, err := Extract(dir, "./...")
		if err != nil {
			return nil, nil, err
		}
		data, err := encode(m)
		return m, data, err
	}

	linuxManifest, linux, linuxErr := extract("linux")
	darwinManifest, darwin, darwinErr := extract("darwin")

	if linuxErr != nil || darwinErr != nil {
		// Refusing is the other acceptable answer: the tool says what it could
		// not state rather than committing one platform's shape.
		for _, err := range []error{linuxErr, darwinErr} {
			if err != nil && !qaNamesThePlatformDeclaration(err) {
				t.Fatalf("refused without naming the platform-specific declaration it could not state: %v", err)
			}
		}
		return
	}

	if bytes.Equal(linux, darwin) {
		return
	}

	fields := func(m *Manifest) string {
		for _, c := range m.Codecs {
			if c.Name == "stamp.created" {
				return strings.Join(c.Fields, ", ")
			}
		}
		return "the codec is missing"
	}
	t.Fatalf("one source tree produced two manifests that disagree about a stored type's JSON shape, and both runs exited 0:\n"+
		"  on linux:  %s\n"+
		"  elsewhere: %s\n"+
		"so the committed manifest is stale on whichever host did not generate it, -base reports the other platform's shape as 'changed shape without a version bump', and the wire difference between the two builds is never shown",
		fields(linuxManifest), fields(darwinManifest))
}
