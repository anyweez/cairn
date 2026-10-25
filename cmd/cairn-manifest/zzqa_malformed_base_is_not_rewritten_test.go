package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// t2885's remaining case: run() reads the base before writing, but still
// writes before it parses and compares it. When -o and -base name the same
// file, a base the run cannot parse is replaced by the manifest the run
// produced, so the same command fails on the first run and passes on the
// second -- exactly as it did for a base the run found incompatible.
func TestQAMalformedBaseIsNotRewrittenByAFailedRun(t *testing.T) {
	dir := copyGoodFixture(t)
	base := filepath.Join(dir, "spec.manifest.yaml")
	malformed := []byte("codecs: [\n")
	if err := os.WriteFile(base, malformed, 0o644); err != nil {
		t.Fatal(err)
	}

	first := run(dir, "spec.manifest.yaml", base, []string{"./..."})
	if first == nil {
		t.Fatal("a base that cannot be parsed must fail the run")
	}

	after, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(malformed) {
		t.Errorf("the failed run replaced the base it could not parse with the manifest it generated")
	}

	second := run(dir, "spec.manifest.yaml", base, []string{"./..."})
	if second == nil {
		t.Fatal("the same command passed on its second run because the first replaced the malformed base")
	}
	if !strings.Contains(second.Error(), "reading") {
		t.Errorf("the second run failed for a different reason than the first: %v", second)
	}
}
