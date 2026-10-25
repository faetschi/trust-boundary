//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"tbound/supervisor/internal/evalcorpus"
	"testing"
	"testing/fstest"
)

func TestConstructRejectsDuplicateProducerMembers(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructNetCase, constructReadCase}})
	for _, tc := range []struct {
		name, id, check, file string
		edit                  func([]byte) []byte
	}{
		{"accept", constructReadCase, "acceptance-cell", w.mapping.Cases[constructReadCase].Reference, func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"passed":true`), []byte(`"passed":false,"passed":true`), 1)
		}},
		{"journal", constructNetCase, "calibration-p", w.mapping.Journal, func(b []byte) []byte { return bytes.Replace(b, []byte(`"seq":0`), []byte(`"seq":99,"seq":0`), 1) }},
		{"net", constructNetCase, "effect-satisfiable", "", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"schema":`), []byte(`"schema":"other","schema":`), 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := w.copyEvidence()
			file := filepath.Join(ev, tc.file)
			if tc.name == "net" {
				entries, err := os.ReadDir(filepath.Join(ev, w.mapping.NetDir))
				if err != nil {
					t.Fatal(err)
				}
				file = filepath.Join(ev, w.mapping.NetDir, entries[0].Name())
			}
			editFile(t, file, tc.edit)
			wantRow(t, rowOf(t, w.apply(ev, tc.id), tc.id, tc.check), "guest-only", "")
		})
	}
}

func TestConstructRejectsStaleAcceptanceIdentity(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructReadCase}})
	for _, field := range []string{"commit", "binary", "image", "corpus"} {
		t.Run(field, func(t *testing.T) {
			e := w.expect
			in := w.input()
			switch field {
			case "commit":
				e.Commit = strings.Repeat("b", 40)
			case "binary":
				e.RunnerDigest = geDigest("other binary")
			case "image":
				e.ImageDigest = geDigest("other image")
			case "corpus":
				in.Manifest = w.m
				in.Manifest.Cases = slices.Clone(w.m.Cases)
				in.Manifest.Cases[0].CaseID += "-other"
			}
			if field == "corpus" {
				// A different manifest with unchanged acceptance artifacts must still be refused.
				ctx := &constructCtx{in: in, files: map[string]constructFile{}}
				ctx.in.Set = os.DirFS(w.ev)
				var cs evalcorpus.Case
				for _, c := range w.m.Cases {
					if c.CaseID == constructReadCase {
						cs = c
					}
				}
				if _, _, err := ctx.acceptOutcome(&constructUse{ctx: ctx}, w.mapping.Cases[constructReadCase], cs); err == nil {
					t.Fatal("accepted another corpus")
				}
				return
			}
			wantRow(t, rowOf(t, w.applyWith(w.ev, e, constructReadCase), constructReadCase, "acceptance-cell"), "guest-only", "")
		})
	}
}

func TestConstructRejectsMissingAcceptanceIdentityAndInconsistentAggregate(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructReadCase}})
	for _, field := range []string{"commit", "binary_sha256", "image_digest", "manifest_digest", "passed"} {
		t.Run(field, func(t *testing.T) {
			ev := w.copyEvidence()
			editFile(t, filepath.Join(ev, w.mapping.Cases[constructReadCase].Reference), func(b []byte) []byte {
				var doc map[string]any
				if err := json.Unmarshal(b, &doc); err != nil {
					t.Fatal(err)
				}
				if field == "passed" {
					doc[field] = false
				} else {
					delete(doc, field)
				}
				raw, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			})
			wantRow(t, rowOf(t, w.apply(ev, constructReadCase), constructReadCase, "acceptance-cell"), "guest-only", "")
		})
	}
}

func TestConstructArtifactProvenanceHashesFileBytes(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructReadCase}})
	row := rowOf(t, w.apply(w.ev, constructReadCase), constructReadCase, "acceptance-cell")
	for _, source := range row.Provenance {
		if !bytes.HasPrefix([]byte(source.Path), []byte("artifacts/")) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(constructPilotArtifacts, filepath.Base(source.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if want := sha256Prefixed(raw); source.SHA256 != want {
			t.Errorf("%s provenance = %s, file hash = %s", source.Path, source.SHA256, want)
		}
	}
}

func TestConstructProvenanceAllowsFullFixtureSize(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructReadCase}})
	cs := w.cases[constructReadCase]
	name := "fixture-" + strings.TrimPrefix(cs.Compliant.WorkspaceFixtureDigest, "sha256:") + ".json"
	arts := fstest.MapFS{}
	entries, err := os.ReadDir(constructPilotArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(constructPilotArtifacts, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name() == name {
			raw = append(raw, bytes.Repeat([]byte(" "), 1<<20)...)
		}
		arts[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	in := w.input()
	in.Artifacts, in.Set = arts, os.DirFS(w.ev)
	ctx := &constructCtx{in: in, files: map[string]constructFile{}}
	status, _, err := ctx.acceptOutcome(&constructUse{ctx: ctx}, w.mapping.Cases[constructReadCase], cs)
	if err != nil || status != "pass" {
		t.Fatalf("valid large fixture: %s %v", status, err)
	}
}

func TestConstructWSRequiresRecomputableInputs(t *testing.T) {
	ids := []string{"PILOT-ADV-A-03", "PILOT-ADV-A-04", "PILOT-ADV-B-02", "PILOT-ADV-B-04"}
	w := buildWorld(t, worldOpts{only: ids})
	if err := os.RemoveAll(filepath.Join(w.ev, "run", "ws-evidence")); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		wantRow(t, rowOf(t, w.apply(w.ev, id), id, "calibration-p"), "guest-only", "")
	}
}
