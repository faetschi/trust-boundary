package ipc

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// adapter/src/ipc-transport.ts repeats the Go transport limits as literals. This test reads that source
// and holds each constant to the Go value, so a one-sided change fails here instead of on the wire.

var tsConstRE = regexp.MustCompile(`(?m)^export const (IPC_\w+) = ("[^"]*"|[A-Z0-9_ +*]+);`)

// tsConstants evaluates the adapter's IPC_ constants: numbers with "_" separators, "+", "*" and earlier names.
func tsConstants(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "adapter", "src", "ipc-transport.ts"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range tsConstRE.FindAllStringSubmatch(string(data), -1) {
		expr := strings.TrimSpace(m[2])
		if strings.HasPrefix(expr, `"`) {
			out[m[1]] = strings.Trim(expr, `"`)
			continue
		}
		sum := uint64(0)
		for _, term := range strings.Split(expr, "+") {
			product := uint64(1)
			for _, factor := range strings.Split(term, "*") {
				factor = strings.TrimSpace(factor)
				if known, ok := out[factor]; ok {
					factor = known
				}
				factor = strings.ReplaceAll(factor, "_", "")
				n, err := strconv.ParseUint(factor, 10, 64)
				if err != nil {
					t.Fatalf("%s: cannot evaluate %q: %v", m[1], expr, err)
				}
				product *= n
			}
			sum += product
		}
		out[m[1]] = strconv.FormatUint(sum, 10)
	}
	return out
}

func TestAdapterTransportLimitsMatchGo(t *testing.T) {
	ts := tsConstants(t)
	want := map[string]string{
		"IPC_WIRE_VERSION":             WireVersion,
		"IPC_PREFIX_BYTES":             strconv.Itoa(PrefixBytes),
		"IPC_MAX_MESSAGE_BYTES":        strconv.Itoa(MaxMessageBytes),
		"IPC_MAX_FRAME_BYTES":          strconv.Itoa(MaxFrameBytes),
		"IPC_MAX_FRAMES_PER_DIRECTION": strconv.Itoa(MaxFramesPerDirection),
		"IPC_MAX_STREAM_BYTES":         strconv.Itoa(MaxStreamBytes),
		"IPC_BINDING_TOKEN_BYTES":      strconv.Itoa(BindingTokenBytes),
		"IPC_BINDING_TOKEN_HEX_BYTES":  strconv.Itoa(BindingTokenHexBytes),
	}
	for name, goValue := range want {
		if got, ok := ts[name]; !ok || got != goValue {
			t.Errorf("adapter %s = %q, Go = %q", name, got, goValue)
		}
	}
}
