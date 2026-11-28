package main

// A -base check exists to say whether entries already in a ledger stay
// readable. A codec is keyed in the manifest by sum type and name, and the
// ledger stores only the name, so Incompatible follows a codec whose sum path
// was renamed instead of reporting every entry it stored as removed.
//
// That fallback also accepts a codec that moved from one sum to another, which
// is a different edit with a different consequence: a stored entry is decoded
// through the codec of the sum it was stored in, so a type that moved leaves
// the sum it left unable to read what it stored. `codec: unknown type
// "moved.published"` is what a load of the old stream returns.
//
// The two edits are told apart by the manifests themselves: a renamed sum
// appears nowhere in the new one, while a sum something moved out of still
// holds its other codecs.

import (
	"path/filepath"
	"strings"
	"testing"
)

const qaMovedSumsGoMod = `module example.com/moved

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `

// qaWriteMovedSumsModule writes the module in one of three states:
//
//	both   - moved.published and moved.drafted are messages, in sum Message
//	moved  - moved.published is registered in the new sum Admin, which is
//	         declared beside Message; Message keeps moved.drafted
//	renamed - the same two codecs, with the sum type renamed Message -> Events
func qaWriteMovedSumsModule(t *testing.T, state string) string {
	t.Helper()
	files := map[string]string{"events.go": qaMovedSumsBoth}
	switch state {
	case "moved":
		files["events.go"] = qaMovedSumsMoved
	case "renamed":
		files["events.go"] = qaMovedSumsRenamed
	}
	return writeModule(t, qaMovedSumsGoMod+mustRepoRoot(t)+"\n", files)
}

const qaMovedSumsBoth = `package moved

import "github.com/anyweez/cairn/codec"

type Message interface{ isMessage() }

type Published struct {
	Title string ` + "`json:\"title\"`" + `
}

func (Published) isMessage() {}

type Drafted struct {
	Title string ` + "`json:\"title\"`" + `
}

func (Drafted) isMessage() {}

func Codecs() *codec.Codec[Message] {
	c := codec.New[Message]()
	codec.Register[Published](c, "moved.published", 1)
	codec.Register[Drafted](c, "moved.drafted", 1)
	return c
}
`

const qaMovedSumsMoved = `package moved

import "github.com/anyweez/cairn/codec"

// Message is still the sum the stream was written with, and still holds a
// codec: Published was moved out of it into Admin.
type Message interface{ isMessage() }

type Admin interface{ isAdmin() }

type Published struct {
	Title string ` + "`json:\"title\"`" + `
}

func (Published) isAdmin() {}

type Drafted struct {
	Title string ` + "`json:\"title\"`" + `
}

func (Drafted) isMessage() {}

func MessageCodecs() *codec.Codec[Message] {
	c := codec.New[Message]()
	codec.Register[Drafted](c, "moved.drafted", 1)
	return c
}

func AdminCodecs() *codec.Codec[Admin] {
	c := codec.New[Admin]()
	codec.Register[Published](c, "moved.published", 1)
	return c
}
`

const qaMovedSumsRenamed = `package moved

import "github.com/anyweez/cairn/codec"

type Events interface{ isEvent() }

type Published struct {
	Title string ` + "`json:\"title\"`" + `
}

func (Published) isEvent() {}

type Drafted struct {
	Title string ` + "`json:\"title\"`" + `
}

func (Drafted) isEvent() {}

func Codecs() *codec.Codec[Events] {
	c := codec.New[Events]()
	codec.Register[Published](c, "moved.published", 1)
	codec.Register[Drafted](c, "moved.drafted", 1)
	return c
}
`

func TestQACodecMovedBetweenSumsFailsTheBaseCheck(t *testing.T) {
	// The manifest of the tree as it was when the entries were stored.
	before := qaWriteMovedSumsModule(t, "both")
	base := filepath.Join(before, "before.yaml")
	if err := run(before, "before.yaml", "", []string{"./..."}); err != nil {
		t.Fatalf("writing the base manifest: %v", err)
	}

	// The defect: moved.published is now registered in sum Admin, with the
	// Message sum still declared and still holding moved.drafted. Nothing
	// about the stored type changed, but a load of the old stream decodes its
	// messages through the Message codec, which no longer has the name.
	moved := qaWriteMovedSumsModule(t, "moved")
	err := run(moved, "moved-after.yaml", base, []string{"./..."})
	if err == nil {
		t.Error("moving moved.published out of example.com/moved.Message passed -base, though the Message codec can no longer decode the messages it stored")
	} else if !strings.Contains(err.Error(), "moved.published") {
		t.Errorf("the base check failed for a different reason: %v", err)
	}

	// The control: renaming the sum type itself keeps every codec, so the
	// entries stay readable and -base must keep passing it.
	renamed := qaWriteMovedSumsModule(t, "renamed")
	if err := run(renamed, "renamed-after.yaml", base, []string{"./..."}); err != nil {
		t.Errorf("a renamed sum type is still readable, so -base must pass it: %v", err)
	}
}
