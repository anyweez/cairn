package main

// The stored type itself declared once per platform, rather than one of its
// fields' types: the ordinary Go way to give a value a platform-specific
// representation. The registration is unconstrained, so the codec is in the
// manifest on every host, but the shape it records is whichever declaration the
// host's build selected. One tree then has two shapes, the committed file is
// stale on every host that did not generate it, and -base calls the other
// platform's declaration a wire change without a version bump.
//
// As with every declaration the extractor cannot state exactly, either answer
// is acceptable: the same manifest on every host, or a refusal naming the
// declarations that disagree. Picking one host's shape and exiting 0 is not.
//
// This is the case t3090's sixth note describes; the reproduction it names
// (zzqa_stored_shape_is_the_same_on_every_host_test.go) took its filename into
// the tree first for the field-type half, so this one carries a different name.

import (
	"bytes"
	"strings"
	"testing"
)

// qaPayloadModule writes a module whose stored type is declared once per
// platform, with a different field in each declaration, and registers it in an
// unconstrained file so the codec exists in every build.
func qaPayloadModule(t *testing.T) string {
	t.Helper()
	return writeModule(t, `module example.com/payloadsplit

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"events.go": `package payloadsplit

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Payload, Event](c, "payload.stored", 1)
	return c
}
`,
		"payload_linux.go": `//go:build linux

package payloadsplit

// Payload carries a path in the ledger on this platform.
type Payload struct {
	ID   string ` + "`json:\"id\"`" + `
	Path string ` + "`json:\"path\"`" + `
}

func (Payload) isEvent() {}
`,
		"payload_other.go": `//go:build !linux

package payloadsplit

// Payload carries a name in the ledger on this platform.
type Payload struct {
	ID   string ` + "`json:\"id\"`" + `
	Name string ` + "`json:\"name\"`" + `
}

func (Payload) isEvent() {}
`,
	})
}

// qaNamesPayloadDeclaration reports whether err names the type declared per
// platform, or either file that declares it.
func qaNamesPayloadDeclaration(err error) bool {
	msg := err.Error()
	for _, want := range []string{"Payload", "payload_linux.go", "payload_other.go"} {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

func TestQAStoredTypeDeclaredOncePerPlatformIsTheSameOnEveryHost(t *testing.T) {
	dir := qaPayloadModule(t)

	extract := func(goos string) ([]byte, error) {
		t.Setenv("GOOS", goos)
		m, err := Extract(dir, "./...")
		if err != nil {
			return nil, err
		}
		return encode(m)
	}

	linux, linuxErr := extract("linux")
	darwin, darwinErr := extract("darwin")

	if linuxErr != nil || darwinErr != nil {
		if (linuxErr == nil) != (darwinErr == nil) {
			t.Fatalf("the same tree was read on one host and refused on the other: on linux: %v; elsewhere: %v", linuxErr, darwinErr)
		}
		for _, err := range []error{linuxErr, darwinErr} {
			if !qaNamesPayloadDeclaration(err) {
				t.Fatalf("refused without naming the type declared once per platform: %v", err)
			}
		}
		return
	}

	if !bytes.Equal(linux, darwin) {
		t.Fatalf("one source tree produced two manifests for a stored type declared once per platform, and both runs exited 0:\n%s\n---\n%s", linux, darwin)
	}
}
