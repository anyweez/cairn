package main

import (
	"os"
	"path/filepath"
	"testing"
)

// -o defaults to spec.manifest.yaml in -dir, and -base is the file the check
// exists to compare against, so the natural local invocation names the same
// file twice:
//
//	go -C cmd/cairn-manifest run . -dir ../.. -base ../../spec.manifest.yaml
//
// The base is read before the write (t2885), so the first run compares
// correctly and fails on a stored type that changed shape without a version
// bump. But run() then writes the manifest anyway, so the file it compared
// against is replaced by the output that failed the check. The next run reads
// that output as its base and reports the change as compatible: a check that
// protects entries already in the ledger passes on the second try with nothing
// fixed.
func TestQAFailedBaseCheckDoesNotRewriteTheBase(t *testing.T) {
	dir := copyGoodFixture(t)
	manifest := filepath.Join(dir, "spec.manifest.yaml")
	if err := os.WriteFile(manifest, []byte(storeBase), 0o644); err != nil {
		t.Fatal(err)
	}

	// storeBase has good.published reshaped since; the same file is both the
	// output to write and the base to check.
	if err := run(dir, "spec.manifest.yaml", manifest, []string{"./..."}); err == nil {
		t.Fatal("the reshaped codec was reported as compatible")
	}
	after, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == storeBase {
		// The base survived the failed check, so a second run is a real
		// second check of the same tree.
		if err := run(dir, "spec.manifest.yaml", manifest, []string{"./..."}); err == nil {
			t.Error("the second run passed: the check is not idempotent")
		}
		return
	}
	// The failed run overwrote the base with the manifest it rejected, so the
	// second run compares that manifest with itself.
	if err := run(dir, "spec.manifest.yaml", manifest, []string{"./..."}); err == nil {
		t.Error("the base was overwritten by the failed run's output, so running the check again reports the incompatible change as compatible")
	}
}
