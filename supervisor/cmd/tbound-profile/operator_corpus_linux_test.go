//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"tbound/supervisor/internal/evalcorpus"
	"testing"
)

func TestOperatorCorpusDigestArgumentsRunPilotNodeChecks(t *testing.T) {
	raw, err := os.ReadFile(constructPilotManifest)
	if err != nil {
		t.Fatal(err)
	}
	m, err := evalcorpus.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Cases {
		m.Cases[i].Signoff = fixtureSignoff()
	}
	m, panel := signedPanel(t, t.TempDir(), m)
	path := writeManifest(t, t.TempDir(), "manifest.json", m)
	substitutions := map[string]string{"--manifest": path, "--artifacts": "../../../corpus/pilot/artifacts", "--node": constructTestNode(t)}
	for i := 0; i < len(panel); i += 2 {
		substitutions[panel[i]] = panel[i+1]
	}
	source, err := os.ReadFile("../../../infra/guest/operator/tbound-op")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, line := range strings.Split(string(source), "\n") {
		start := strings.Index(line, " corpus-digest ")
		if start < 0 {
			continue
		}
		calls++
		t.Run(fmt.Sprint(calls), func(t *testing.T) {
			line = strings.Split(line[start+len(" corpus-digest "):], ") ||")[0]
			var args []string
			fields := strings.Fields(line)
			node := ""
			for i := 0; i < len(fields); i++ {
				if fields[i] == "2>&1" {
					continue
				}
				flag := fields[i]
				i++
				if i == len(fields) {
					t.Fatal("unpaired operator flag", flag)
				}
				value := strings.Trim(fields[i], "\"")
				if flag == "--node" {
					node = value
				}
				if replacement, ok := substitutions[flag]; ok {
					value = replacement
				}
				args = append(args, flag, value)
			}
			got, stderr, err := runCLI(append([]string{"corpus-digest"}, args...)...)
			if err != nil {
				t.Fatalf("operator pilot admission: %v %s", err, stderr)
			}
			want, _ := evalcorpus.Digest(m)
			if got != want+"\n" {
				t.Fatal("wrong corpus digest", got)
			}
			if node != "/usr/local/bin/node" {
				t.Fatalf("operator node pin %q", node)
			}
		})
	}
	if calls != 3 {
		t.Fatalf("tested %d operator calls; want 3", calls)
	}
}
