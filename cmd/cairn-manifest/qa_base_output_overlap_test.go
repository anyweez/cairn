package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A -base check must be decided from the base file as it was before the run
// wrote anything. -o defaults to spec.manifest.yaml in -dir, so the natural
// local invocation -- regenerate the committed manifest and check the tree
// against it -- names the same file twice:
//
//	go -C cmd/cairn-manifest run . -dir ../.. -base ../../spec.manifest.yaml
//
// run() writes the manifest and only then reads the base, so that invocation
// compares the manifest it just wrote with itself: it reports the stored types
// as compatible whatever changed, and the base it was asked to compare against
// has been overwritten. The CI job avoids it only because it writes to stdout
// (-o -) and keeps the base in $RUNNER_TEMP.
//
// storeBase is a manifest whose only codec was reshaped since, which is what
// the check exists to catch. Against a base at any other path the same bytes
// fail with "changed shape without a version bump", which is the second half
// of this test.
const storeBase = "codecs:\n" +
	"  - sum: example.com/good.Event\n" +
	"    name: good.published\n" +
	"    version: 3\n" +
	"    upcasts: [1, 2]\n" +
	"    fields:\n" +
	"      - 'gone: int'\n"

func TestABaseThatIsAlsoTheOutputIsNotChecked(t *testing.T) {
	dir := copyGoodFixture(t)
	manifest := filepath.Join(dir, "spec.manifest.yaml")
	if err := os.WriteFile(manifest, []byte(storeBase), 0o644); err != nil {
		t.Fatal(err)
	}

	// -o is relative to -dir and names the same file as -base.
	err := run(dir, "spec.manifest.yaml", manifest, []string{"./..."})
	if err == nil {
		t.Error("the run compared its own output with itself and reported the stored types as compatible")
	}

	// The same base kept outside the run's output path is still checked.
	base := filepath.Join(t.TempDir(), "old.yaml")
	if err := os.WriteFile(base, []byte(storeBase), 0o644); err != nil {
		t.Fatal(err)
	}
	err = run(dir, "spec.manifest.yaml", base, []string{"./..."})
	if err == nil || !strings.Contains(err.Error(), "changed shape without a version bump") {
		t.Errorf("a distinct base must still fail the reshaped codec, got %v", err)
	}
}

// copyGoodFixture copies testdata/good into a temporary module with the
// replace directive pointed at this checkout, so a test can write a manifest
// beside it without leaving a file in the repository.
func copyGoodFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	src, err := filepath.Abs(filepath.Join("testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	for _, name := range []string{"go.mod", "good.go", "good_test.go"} {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" {
			data = []byte(strings.Replace(string(data), "../../../..", root, 1))
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}
