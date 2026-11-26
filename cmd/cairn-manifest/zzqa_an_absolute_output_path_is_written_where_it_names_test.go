package main

// run joins -o onto -dir before writing, so an absolute -o is re-rooted under
// the module: filepath.Join("/repo", "/tmp/out.yaml") is "/repo/tmp/out.yaml",
// not "/tmp/out.yaml". When that directory exists in the module the run writes
// the manifest there and exits 0, so a caller that regenerates into a temp
// file and diffs it reads a stale file and believes the manifest is current.
// When it does not exist the run fails naming a path the caller never passed.
//
// The natural local invocation writes into -dir by default, so this is only
// visible with an explicit output path, and the flag's help calls the value
// "relative to -dir" rather than saying an absolute one is refused.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQAAnAbsoluteOutputPathIsWrittenWhereItNames(t *testing.T) {
	dir := copyGoodFixture(t)
	out := filepath.Join(t.TempDir(), "spec.manifest.yaml")

	// A module with the directory the join lands in, which is what turns the
	// bug from a confusing error into a run that exits 0 having written
	// somewhere else. A module with a tmp/ directory is the ordinary case.
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, out)), 0o755); err != nil {
		t.Fatal(err)
	}

	err := run(dir, out, "", []string{"./..."})
	if err != nil {
		// Refusing an absolute value is acceptable. Writing the manifest
		// under -dir is not, whatever the run reports.
		if _, statErr := os.Stat(filepath.Join(dir, out)); statErr == nil {
			t.Fatalf("the run refused %q but wrote it under -dir instead", out)
		}
		return
	}
	if _, statErr := os.Stat(out); statErr != nil {
		t.Fatalf("the run reported success but wrote no manifest at %q: %v", out, statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, out)); statErr == nil {
		t.Fatalf("the run wrote the manifest at %q, under -dir, instead of at %q",
			filepath.Join(dir, out), out)
	}
}
