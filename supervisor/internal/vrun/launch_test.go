package vrun

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchScriptIsValidBashAndWritesPisProviderConfig(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	if out, err := exec.Command(bash, "-n", "-c", launchScript).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	head, _, found := strings.Cut(launchScript, "\n\"$1\" -e")
	if !found {
		t.Fatal("the launch script no longer starts the relay after writing models.json")
	}
	home := t.TempDir()
	cmd := exec.Command(bash, "-c", head+"\ncat \"$HOME/.pi/agent/models.json\"", "pi-v", "node", "cli.js", "deepseek/deepseek-v4.1-flash", "prompt")
	cmd.Env = append(os.Environ(), "HOME="+home, "TMPDIR="+filepath.Join(home, "tmp"))
	out, err := cmd.CombinedOutput()
	want := `{"providers":{"research":{"baseUrl":"http://127.0.0.1:18081/v1","api":"openai-completions","apiKey":"$TBOUND_V_KEY","models":[{"id":"deepseek/deepseek-v4.1-flash"}]}}}`
	if err != nil || strings.TrimSpace(string(out)) != want {
		t.Fatalf("models.json = %q (%v), want %q", out, err, want)
	}
}
