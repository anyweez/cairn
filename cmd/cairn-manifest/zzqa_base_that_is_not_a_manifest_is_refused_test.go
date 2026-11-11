package main

// Another case of t3333: run() reads the base before it writes anything, but it
// trusts whatever YAML it finds there. A base file that is empty, or one whose
// write stopped before its codecs section (the manifest is written rules first,
// codecs second), parses as a document with no codecs. Incompatible cannot tell
// that from a manifest that legitimately lists none, so the run reports no
// incompatibility, writes the manifest and exits 0.
//
// The check is then silently vacuous: a stored type removed, downgraded or
// reshaped since that base passes it, where the same base with its codecs
// section reports the break. The control below is exactly that base, so a run
// that fails for an unrelated reason cannot pass this test.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQABaseThatIsNotAManifestIsRefused(t *testing.T) {
	dir := copyGoodFixture(t)

	// The control: a complete but incompatible base fails with the message the
	// check exists to produce.
	incompatible := filepath.Join(t.TempDir(), "incompatible.yaml")
	if err := os.WriteFile(incompatible, []byte(storeBase), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run(dir, "out-control.yaml", incompatible, []string{"./..."})
	if err == nil || !strings.Contains(err.Error(), "changed shape without a version bump") {
		t.Fatalf("an incompatible complete base must fail the run, got %v", err)
	}

	// A base generated from this module, so the truncations below are the file
	// an interrupted write leaves behind.
	if err := run(dir, "full.yaml", "", []string{"./..."}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	codecsAt := strings.Index(string(raw), "\ncodecs:")
	if codecsAt < 0 {
		t.Fatalf("the generated manifest has no codecs section:\n%s", raw)
	}

	cases := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"comment only", "# the write stopped before anything was written\n"},
		{"rules only, truncated before its codecs section", string(raw[:codecsAt+1])},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "base.yaml")
			if err := os.WriteFile(base, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			out := "out-" + string(rune('a'+i)) + ".yaml"
			err := run(dir, out, base, []string{"./..."})
			if err == nil {
				t.Fatalf("a base that is not a manifest passed the check silently, so a stored type changed since it would go unreported")
			}
			if !strings.Contains(err.Error(), "base.yaml") {
				t.Errorf("a refused base must be named in the error, got %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, out)); statErr == nil {
				t.Errorf("a refused base still let the run write the manifest")
			}
		})
	}
}
