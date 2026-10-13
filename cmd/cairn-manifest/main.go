// Command cairn-manifest writes a module's spec to a committed JSON file:
// every rule, every codec type stored in the ledger with its JSON shape, and
// every fsm table. A review then reads changes to the spec as a diff of that
// file rather than hunting for them in code.
//
//	cairn-manifest [-dir root] [-o spec.manifest.json] [-base old.json] [packages]
//
// Rules and tables are package-level variables, read from every file
// including tests. Codecs are codec.Register calls outside test files, since
// only production code writes to the ledger.
//
// With -base, the manifest is also compared with an earlier one, and the
// command fails if a stored type changed in a way that breaks reading what is
// already in the ledger: removed, moved back a version, reshaped without a
// version bump, or bumped without an upcaster.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

func main() {
	dir := flag.String("dir", ".", "the module to read")
	out := flag.String("o", "spec.manifest.json", "the manifest to write, relative to -dir; - for stdout")
	base := flag.String("base", "", "an earlier manifest to check codec changes against")
	flag.Parse()
	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	if err := run(*dir, *out, *base, patterns); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dir, out, base string, patterns []string) error {
	m, err := Extract(dir, patterns...)
	if err != nil {
		return err
	}
	data, err := encode(m)
	if err != nil {
		return err
	}
	if out == "-" {
		_, err = os.Stdout.Write(data)
	} else {
		err = os.WriteFile(filepath.Join(dir, out), data, 0o644)
	}
	if err != nil || base == "" {
		return err
	}

	raw, err := os.ReadFile(base)
	if err != nil {
		return err
	}
	var old Manifest
	if err := json.Unmarshal(raw, &old); err != nil {
		return fmt.Errorf("reading %s: %w", base, err)
	}
	if problems := Incompatible(&old, m); len(problems) > 0 {
		var b bytes.Buffer
		fmt.Fprintf(&b, "stored types changed incompatibly since %s:", base)
		for _, p := range problems {
			fmt.Fprintf(&b, "\n  %s", p)
		}
		return fmt.Errorf("%s", b.String())
	}
	return nil
}

func encode(m *Manifest) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(m)
	return b.Bytes(), err
}

// Incompatible lists the codec changes from old to m that would break reading
// entries already stored under old.
func Incompatible(old, m *Manifest) []string {
	now := map[string]Codec{}
	for _, c := range m.Codecs {
		now[c.key()] = c
	}
	var problems []string
	for _, was := range old.Codecs {
		c, ok := now[was.key()]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s (%s) was removed; entries already stored with it can't be decoded", was.Name, was.Sum))
		case c.Version < was.Version:
			problems = append(problems, fmt.Sprintf("%s went back from version %d to %d", c.Name, was.Version, c.Version))
		case c.Version == was.Version && !slices.Equal(c.Fields, was.Fields):
			problems = append(problems, fmt.Sprintf("%s changed shape without a version bump (still version %d)", c.Name, c.Version))
		default:
			for v := was.Version; v < c.Version; v++ {
				if !slices.Contains(c.Upcasts, v) {
					problems = append(problems, fmt.Sprintf("%s is now version %d but has no upcaster from version %d", c.Name, c.Version, v))
				}
			}
		}
	}
	return problems
}
