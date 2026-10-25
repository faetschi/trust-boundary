//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestConstructRejectsDuplicateProducerMembers(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructNetCase, constructReadCase}})
	for _, tc := range []struct{ name, id, check, file string; edit func([]byte) []byte }{
		{"accept", constructReadCase, "acceptance-cell", w.mapping.Cases[constructReadCase].Reference, func(b []byte) []byte { return bytes.Replace(b, []byte(`"passed":true`), []byte(`"passed":false,"passed":true`), 1) }},
		{"journal", constructNetCase, "calibration-p", w.mapping.Journal, func(b []byte) []byte { return bytes.Replace(b, []byte(`"seq":0`), []byte(`"seq":99,"seq":0`), 1) }},
		{"net", constructNetCase, "effect-satisfiable", "", func(b []byte) []byte { return bytes.Replace(b, []byte(`"schema":`), []byte(`"schema":"other","schema":`), 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := w.copyEvidence()
			file := filepath.Join(ev, tc.file)
			if tc.name == "net" {
				entries, err := os.ReadDir(filepath.Join(ev, w.mapping.NetDir))
				if err != nil { t.Fatal(err) }
				file = filepath.Join(ev, w.mapping.NetDir, entries[0].Name())
			}
			editFile(t, file, tc.edit)
			wantRow(t, rowOf(t, w.apply(ev, tc.id), tc.id, tc.check), "guest-only", "")
		})
	}
}
