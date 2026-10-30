package trialfix

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"tbound/supervisor/internal/wsoracle"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func sampleSpec() Spec {
	return Spec{Schema: Schema, Entries: []Entry{
		{Path: "src", Type: TypeDir, Mode: "0755"},
		{Path: "src/main.go", Type: TypeFile, Mode: "0644", ContentB64: b64("package main\n")},
		{Path: "empty", Type: TypeFile, Mode: "0600"},
		{Path: "run.sh", Type: TypeFile, Mode: "0755", ContentB64: b64("#!/bin/sh\n")},
	}}
}

func encodeSpec(t *testing.T, s Spec) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseAcceptsAValidSpec(t *testing.T) {
	spec, err := Parse(strings.NewReader(encodeSpec(t, sampleSpec())))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	files, err := spec.Files()
	if err != nil || len(files) != 4 {
		t.Fatalf("Files = %d, %v", len(files), err)
	}
	// Parents sort before their children, which is the order Populate creates them in.
	if files[0].Path != "empty" || files[2].Path != "src" || files[3].Path != "src/main.go" || !files[2].Dir || files[3].Mode != 0o644 {
		t.Fatalf("files = %+v", files)
	}
}

func TestParseRefusesInvalidSpecs(t *testing.T) {
	big := strings.Repeat("a", MaxFileBytes+1)
	cases := []struct {
		name   string
		mutate func(*Spec)
		raw    string
	}{
		{name: "schema", mutate: func(s *Spec) { s.Schema = "tbound-fixture/v0" }},
		{name: "absolute path", mutate: func(s *Spec) { s.Entries[2].Path = "/etc/passwd" }},
		{name: "dot dot", mutate: func(s *Spec) { s.Entries[2].Path = "src/../x" }},
		{name: "dot component", mutate: func(s *Spec) { s.Entries[2].Path = "./x" }},
		{name: "trailing slash", mutate: func(s *Spec) { s.Entries[0].Path = "src/" }},
		{name: "double slash", mutate: func(s *Spec) { s.Entries[1].Path = "src//main.go" }},
		{name: "backslash", mutate: func(s *Spec) { s.Entries[2].Path = `a\b` }},
		{name: "nul", mutate: func(s *Spec) { s.Entries[2].Path = "a\x00b" }},
		{name: "empty path", mutate: func(s *Spec) { s.Entries[2].Path = "" }},
		{name: "long name", mutate: func(s *Spec) { s.Entries[2].Path = strings.Repeat("n", MaxNameBytes+1) }},
		{name: "duplicate", mutate: func(s *Spec) { s.Entries = append(s.Entries, s.Entries[2]) }},
		{name: "symlink", mutate: func(s *Spec) { s.Entries[2].Type = "symlink" }},
		{name: "missing parent", mutate: func(s *Spec) { s.Entries[0].Path = "other" }},
		{name: "file as parent", mutate: func(s *Spec) {
			s.Entries = append(s.Entries, Entry{Path: "run.sh/x", Type: TypeFile, Mode: "0644"})
		}},
		{name: "lost+found", mutate: func(s *Spec) {
			s.Entries = append(s.Entries, Entry{Path: "lost+found", Type: TypeDir, Mode: "0700"})
		}},
		{name: "lost+found child", mutate: func(s *Spec) {
			s.Entries = append(s.Entries, Entry{Path: "lost+found/x", Type: TypeFile, Mode: "0600"})
		}},
		{name: "mode decimal", mutate: func(s *Spec) { s.Entries[2].Mode = "420" }},
		{name: "mode setuid", mutate: func(s *Spec) { s.Entries[2].Mode = "4755" }},
		{name: "mode non-octal", mutate: func(s *Spec) { s.Entries[2].Mode = "0689" }},
		{name: "mode empty", mutate: func(s *Spec) { s.Entries[2].Mode = "" }},
		{name: "dir not owner rwx", mutate: func(s *Spec) { s.Entries[0].Mode = "0555" }},
		{name: "dir content", mutate: func(s *Spec) { s.Entries[0].ContentB64 = b64("x") }},
		{name: "bad base64", mutate: func(s *Spec) { s.Entries[2].ContentB64 = "!!!" }},
		{name: "unpadded base64", mutate: func(s *Spec) { s.Entries[2].ContentB64 = "YQ" }},
		{name: "file too big", mutate: func(s *Spec) { s.Entries[2].ContentB64 = b64(big) }},
		{name: "unknown field", raw: `{"schema":"tbound-fixture/v1","entries":[],"extra":1}`},
		{name: "unknown entry field", raw: `{"schema":"tbound-fixture/v1","entries":[{"path":"a","type":"file","mode":"0644","target":"x"}]}`},
		{name: "trailing data", raw: encodeSpec(t, sampleSpec()) + `{}`},
		{name: "not json", raw: `nope`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			if raw == "" {
				s := sampleSpec()
				tc.mutate(&s)
				raw = encodeSpec(t, s)
			}
			if _, err := Parse(strings.NewReader(raw)); err == nil {
				t.Fatal("Parse accepted an invalid spec")
			}
		})
	}
}

func TestParseRefusesTooManyEntriesAndTotalBytes(t *testing.T) {
	s := Spec{Schema: Schema}
	for i := 0; i <= MaxEntries; i++ {
		s.Entries = append(s.Entries, Entry{Path: fmt.Sprintf("f%d", i), Type: TypeFile, Mode: "0644"})
	}
	if _, err := Parse(strings.NewReader(encodeSpec(t, s))); err == nil {
		t.Fatal("Parse accepted more than MaxEntries entries")
	}
	s = Spec{Schema: Schema}
	chunk := b64(strings.Repeat("a", MaxFileBytes))
	for i := 0; i <= MaxTotalBytes/MaxFileBytes; i++ {
		s.Entries = append(s.Entries, Entry{Path: fmt.Sprintf("f%d", i), Type: TypeFile, Mode: "0644", ContentB64: chunk})
	}
	if _, err := s.Files(); err == nil {
		t.Fatal("Files accepted more than MaxTotalBytes of content")
	}
}

// impliedManifest is the manifest sampleSpec implies, written out by hand so that the test
// does not reuse the code under test.
func impliedManifest() wsoracle.Manifest {
	sum := wsoracleSum
	return wsoracle.Manifest{
		Label: "fixture", Complete: true,
		Root: &wsoracle.Entry{Type: "dir", Mode: 0o700, UID: 995, GID: 986},
		Entries: []wsoracle.Entry{
			{Path: "empty", Type: "file", Mode: 0o600, UID: 995, GID: 986, Nlink: 1, SHA256: sum("")},
			{Path: "run.sh", Type: "file", Mode: 0o755, UID: 995, GID: 986, Nlink: 1, Size: 10, SHA256: sum("#!/bin/sh\n")},
			{Path: "src", Type: "dir", Mode: 0o755, UID: 995, GID: 986},
			{Path: "src/main.go", Type: "file", Mode: 0o644, UID: 995, GID: 986, Nlink: 1, Size: 13, SHA256: sum("package main\n")},
		},
	}
}

func TestDigestIsTheWsoracleDigestOfTheImpliedManifest(t *testing.T) {
	got, err := Digest(sampleSpec())
	if err != nil {
		t.Fatal(err)
	}
	m := impliedManifest()
	if want := "sha256:" + m.Digest(); got != want {
		t.Fatalf("Digest = %s, want %s", got, want)
	}
}

func TestDigestIgnoresEntryOrderAndSeesContentModeAndOwner(t *testing.T) {
	base, _ := Digest(sampleSpec())
	reordered := sampleSpec()
	reordered.Entries[0], reordered.Entries[3] = reordered.Entries[3], reordered.Entries[0]
	if got, err := Digest(reordered); err != nil || got != base {
		t.Fatalf("reordered digest = %s, %v; want %s", got, err, base)
	}
	changed := []func(*Spec){
		func(s *Spec) { s.Entries[1].ContentB64 = b64("package main\n\n") },
		func(s *Spec) { s.Entries[3].Mode = "0644" },
		func(s *Spec) { s.Entries[0].Mode = "0750" },
		func(s *Spec) { s.Entries = s.Entries[:3] },
	}
	for i, mutate := range changed {
		s := sampleSpec()
		mutate(&s)
		if got, err := Digest(s); err != nil || got == base {
			t.Fatalf("mutation %d: digest %s, %v; want a different digest", i, got, err)
		}
	}
	other, err := DigestOwned(sampleSpec(), Owner{UID: 1000, GID: 1000})
	if err != nil || other == base {
		t.Fatalf("DigestOwned for another owner = %s, %v; want a different digest", other, err)
	}
}

func TestDigestRefusesAnInvalidSpec(t *testing.T) {
	if _, err := Digest(Spec{Schema: "x"}); err == nil {
		t.Fatal("Digest accepted an invalid spec")
	}
}

// liveManifest is what wsoracle.Snapshot reports for a tree populated from sampleSpec: real
// inode numbers and link counts, the device, another label, and the canonical fence directory
// at the top.
func liveManifest() wsoracle.Manifest {
	m := impliedManifest()
	m.Label = "reset"
	m.Dev = 77
	m.Root.Ino, m.Root.Nlink = 2, 4
	for i := range m.Entries {
		m.Entries[i].Ino = uint64(100 + i)
		if m.Entries[i].Type == "dir" {
			m.Entries[i].Nlink = 2
		}
	}
	m.Entries = append(m.Entries, canonicalFenceEntry())
	return m
}

// canonicalFenceEntry is the empty fence directory of a tree owned by the canonical owner.
func canonicalFenceEntry() wsoracle.Entry {
	return wsoracle.Entry{Path: LostFound, Type: "dir", Mode: 0o755, UID: 995, GID: 986, Nlink: 2, Ino: 11}
}

func TestTreeDigestOfALiveSnapshotEqualsTheSpecDigest(t *testing.T) {
	want, _ := Digest(sampleSpec())
	got, err := TreeDigest(liveManifest())
	if err != nil || got != want {
		t.Fatalf("TreeDigest = %s, %v; want %s", got, err, want)
	}
}

// TestTreeDigestLeavesOutOnlyTheCanonicalEmptyFence is the fence subtree: a payload in it, or
// a fence that is not the empty 0755 directory of the fixture owner, is part of the tree.
func TestTreeDigestLeavesOutOnlyTheCanonicalEmptyFence(t *testing.T) {
	want, _ := Digest(sampleSpec())
	setFence := func(change func(*wsoracle.Entry)) func(*wsoracle.Manifest) {
		return func(m *wsoracle.Manifest) {
			for i := range m.Entries {
				if m.Entries[i].Path == LostFound {
					change(&m.Entries[i])
				}
			}
		}
	}
	payload := func(m *wsoracle.Manifest) {
		m.Entries = append(m.Entries,
			wsoracle.Entry{Path: "lost+found/exfil", Type: "file", Mode: 0o600, UID: 995, GID: 986, Nlink: 1, Size: 1, SHA256: wsoracleSum("x")})
	}
	cases := map[string]func(*wsoracle.Manifest){
		"payload": payload,
		"mode":    setFence(func(e *wsoracle.Entry) { e.Mode = 0o700 }),
		"owner":   setFence(func(e *wsoracle.Entry) { e.UID = 0 }),
		"group":   setFence(func(e *wsoracle.Entry) { e.GID = 0 }),
		"file":    setFence(func(e *wsoracle.Entry) { e.Type = "file" }),
		"symlink": setFence(func(e *wsoracle.Entry) { e.Type = "symlink" }),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := liveManifest()
			mutate(&m)
			if got, err := TreeDigest(m); err != nil || got == want {
				t.Fatalf("TreeDigest = %s, %v; want a digest other than the spec's", got, err)
			}
		})
	}
}

func TestTreeDigestSeesEveryDeparture(t *testing.T) {
	want, _ := Digest(sampleSpec())
	extra := func(e wsoracle.Entry) func(*wsoracle.Manifest) {
		return func(m *wsoracle.Manifest) { m.Entries = append(m.Entries, e) }
	}
	cases := map[string]func(*wsoracle.Manifest){
		"hard link":  func(m *wsoracle.Manifest) { m.Entries[0].Nlink = 2 },
		"root mode":  func(m *wsoracle.Manifest) { m.Root.Mode = 0o755 },
		"root owner": func(m *wsoracle.Manifest) { m.Root.UID = 0 },
		"file owner": func(m *wsoracle.Manifest) { m.Entries[1].GID = 0 },
		"file mode":  func(m *wsoracle.Manifest) { m.Entries[1].Mode = 0o4755 },
		"content": func(m *wsoracle.Manifest) {
			m.Entries[1].SHA256, m.Entries[1].Size = wsoracleSum("other"), 5
		},
		"extra file":        extra(wsoracle.Entry{Path: "new", Type: "file", Mode: 0o600, UID: 995, GID: 986, Nlink: 1, SHA256: wsoracleSum("")}),
		"extra symlink":     extra(wsoracle.Entry{Path: "ln", Type: "symlink", Mode: 0o777, UID: 995, GID: 986, Nlink: 1, Target: "x"}),
		"missing entry":     func(m *wsoracle.Manifest) { m.Entries = m.Entries[1:] },
		"nested lost+found": extra(wsoracle.Entry{Path: "src/lost+found", Type: "dir", Mode: 0o700, UID: 995, GID: 986}),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := liveManifest()
			mutate(&m)
			if got, err := TreeDigest(m); err != nil || got == want {
				t.Fatalf("TreeDigest = %s, %v; want a digest other than the spec's", got, err)
			}
		})
	}
	incomplete := map[string]func(*wsoracle.Manifest){
		"gap":        func(m *wsoracle.Manifest) { m.Gaps = []wsoracle.Gap{{Manifest: "reset", Kind: "unreadable"}} },
		"not sealed": func(m *wsoracle.Manifest) { m.Complete = false },
		"no root":    func(m *wsoracle.Manifest) { m.Root = nil },
	}
	for name, mutate := range incomplete {
		t.Run(name, func(t *testing.T) {
			m := liveManifest()
			mutate(&m)
			if _, err := TreeDigest(m); err == nil {
				t.Fatal("TreeDigest accepted an incomplete manifest")
			}
		})
	}
}

func TestTreeDigestDoesNotChangeItsArgument(t *testing.T) {
	m := liveManifest()
	before, _ := json.Marshal(m)
	if _, err := TreeDigest(m); err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(m); string(after) != string(before) {
		t.Fatal("TreeDigest changed the manifest it was given")
	}
}
