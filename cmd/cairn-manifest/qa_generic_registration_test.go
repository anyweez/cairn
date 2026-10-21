package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func qaWriteGenericModule(t *testing.T, dir, field string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	gomod := fmt.Sprintf("module example.com/generic\n\ngo 1.26.0\n\nrequire github.com/anyweez/cairn v0.0.0\n\nreplace github.com/anyweez/cairn => %s\n", root)
	src := fmt.Sprintf(`package generic

import "github.com/anyweez/cairn/codec"

type Msg interface{ isMsg() }

type Event struct{ %s int `+"`json:\"%s\"`"+` }

func (Event) isMsg() {}

func register[V Msg](c *codec.Codec[Msg]) {
	codec.Register[V, Msg](c, "generic.event", 1)
}

func Codecs() *codec.Codec[Msg] {
	c := codec.New[Msg]()
	register[Event](c)
	return c
}
`, field, field)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generic.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestQAGenericRegistrationShape(t *testing.T) {
	oldDir, newDir := t.TempDir(), t.TempDir()
	qaWriteGenericModule(t, oldDir, "Old")
	qaWriteGenericModule(t, newDir, "New")
	oldM, oldErr := Extract(oldDir, "./...")
	newM, newErr := Extract(newDir, "./...")
	if oldErr != nil || newErr != nil {
		t.Logf("tool refused: old=%v new=%v", oldErr, newErr)
		return
	}
	t.Logf("old codecs: %+v", oldM.Codecs)
	t.Logf("new codecs: %+v", newM.Codecs)
	if problems := Incompatible(oldM, newM); len(problems) == 0 {
		t.Fatalf("the stored shape changed from Old to New, but the tool reported no break; both manifests describe the registration as %+v", oldM.Codecs)
	}
}
