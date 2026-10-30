//go:build linux

package piruntime

import (
	"bytes"
	"strings"
	"testing"
)

func TestPiCLIWorkerRPCProfiles(t *testing.T) {
	production, _ := testHostProfile()
	development := developmentPiCLIProfile(DeveloperPiCLIPlan{
		PiPath: "/opt/pi/cli.js", PiSHA256: DigestBytes([]byte("entry")),
		NodeVersion: PiCLIProductionNodeVersion, WorkingDir: "/work",
		Home: "/home/test", TempDir: "/tmp/test", BundleRoot: "/runtime/extension",
	}, DigestBytes([]byte("bundle")), nil)
	for _, profile := range []HostProfile{production, development} {
		t.Run(profile.ChildArgumentsProfile, func(t *testing.T) {
			var out bytes.Buffer
			client, err := newPiCLIWorkerRPC(profile, strings.NewReader(""), &out)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.SendPrompt("request-1", "hello"); err != nil {
				t.Fatal(err)
			}
			if want := "{\"id\":\"request-1\",\"type\":\"prompt\",\"message\":\"hello\"}\n"; out.String() != want {
				t.Fatalf("prompt: got %q, want %q", out.String(), want)
			}
		})
	}
	development.ChildArgumentsProfile = "legacy-sdk-worker-v1"
	var out bytes.Buffer
	if _, err := newPiCLIWorkerRPC(development, strings.NewReader(""), &out); err == nil || out.Len() != 0 {
		t.Fatal("legacy profile was accepted or emitted RPC bytes")
	}
}
