// Command tbound-web is an optional, untrusted, read-only presentation client.
// The CLI remains the required claim-bearing frontend, and this server does not
// participate in supervisor decisions or H1. It is loopback-only because
// browser access outside the local machine needs separate authentication and
// session-management evaluation.
//
// Scope references: MSE_MA_Thesis/agentic-harness/tbound-thesis.md (around lines
// 113, 463, 963); AH-technical/06-1-language-decision.md (lines 11 and 16);
// AH-technical/00-roadmap.md:77; todo-implementation-tbound.md:60; and
// visualizations-plan.md:417,543.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"tbound/supervisor/internal/webview"
)

const defaultAddress = "127.0.0.1:8787"

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string, stderr interface{ Write([]byte) (int, error) }) error {
	flags := flag.NewFlagSet("tbound-web", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("addr", defaultAddress, "loopback HTTP listen address")
	history := flags.String("history", "", "optional bounded viewer-owned observation history JSON path")
	var journals, transcripts, testJSON, evidence pathList
	runManifest := flags.String("run-manifest", "", "read-only versioned go test run lifecycle manifest (requires --test-json)")
	flags.Var(&journals, "journal", "read-only tbound audit JSONL path (up to two; repeatable or comma-separated)")
	flags.Var(&transcripts, "transcript", "read-only supervisor transcript NDJSON path (repeatable or comma-separated)")
	flags.Var(&testJSON, "test-json", "read-only `go test -json` JSONL path (repeatable or comma-separated)")
	flags.Var(&evidence, "evidence-bundle", "read-only sessionrepo evidence bundle JSON path (at most one)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !loopbackAddress(*address) {
		return errors.New("tbound-web has no remote authentication; --addr must use a literal loopback IP address")
	}
	if (*runManifest != "") != (len(testJSON) != 0) {
		return errors.New("--run-manifest and at least one --test-json must be supplied together")
	}
	sources := make([]webview.Source, 0, len(journals)+len(transcripts)+len(testJSON)+len(evidence)+1)
	for index, path := range journals {
		sources = append(sources, webview.Source{Name: sourceName("audit journal", index, len(journals)), Path: path, Kind: "audit"})
	}
	for index, path := range transcripts {
		sources = append(sources, webview.Source{Name: sourceName("supervisor transcript", index, len(transcripts)), Path: path, Kind: "transcript"})
	}
	for index, path := range testJSON {
		sources = append(sources, webview.Source{Name: sourceName("go test JSON stream", index, len(testJSON)), Path: path, Kind: "go-test"})
	}
	if *runManifest != "" {
		sources = append(sources, webview.Source{Name: "go test run manifest", Path: *runManifest, Kind: "manifest"})
	}
	for index, path := range evidence {
		sources = append(sources, webview.Source{Name: sourceName("sessionrepo evidence bundle", index, len(evidence)), Path: path, Kind: "evidence"})
	}
	if len(sources) == 0 {
		return errors.New("at least one --journal, --transcript, --test-json, or --evidence-bundle source is required")
	}
	if *history != "" {
		for _, source := range sources {
			if sameConfiguredPath(*history, source.Path) {
				return errors.New("--history must not name a configured read-only source")
			}
		}
	}

	var buffer *webview.Buffer
	var err error
	if *history == "" {
		buffer = webview.NewBuffer(webview.DefaultBufferCapacity)
	} else {
		buffer, err = webview.NewHistoryBuffer(*history, webview.DefaultBufferCapacity)
		if err != nil {
			return err
		}
	}
	tailer, err := webview.NewTailer(sources, buffer, webview.TailerOptions{})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := tailer.Start(ctx); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		stop()
		<-tailer.Done()
		return fmt.Errorf("listen on %s: %w", *address, err)
	}
	server := &http.Server{
		Handler:           webview.NewHandler(buffer),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	fmt.Fprintf(stderr, "tbound-web listening at http://%s (untrusted presentation client; read-only; loopback only)\n", listener.Addr())

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		stop()
		tailerErr := <-tailer.Done()
		return errors.Join(shutdownErr, tailerErr)
	case err := <-serverDone:
		stop()
		tailerErr := <-tailer.Done()
		if errors.Is(err, http.ErrServerClosed) {
			return tailerErr
		}
		return errors.Join(fmt.Errorf("HTTP server: %w", err), tailerErr)
	case err := <-tailer.Done():
		stop()
		_ = server.Close()
		if err == nil {
			return errors.New("file tailer stopped unexpectedly")
		}
		return fmt.Errorf("file tailer: %w", err)
	}
}

func sameConfiguredPath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr == nil && rightErr == nil {
		if leftAbs == rightAbs || (runtime.GOOS == "windows" && strings.EqualFold(leftAbs, rightAbs)) {
			return true
		}
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

type pathList []string

func (p *pathList) String() string { return strings.Join(*p, ",") }

func (p *pathList) Set(value string) error {
	parts := strings.Split(value, ",")
	for _, part := range parts {
		path := strings.TrimSpace(part)
		if path == "" {
			return errors.New("source paths must not be empty")
		}
		*p = append(*p, path)
	}
	return nil
}

func sourceName(label string, index, total int) string {
	if total == 1 {
		return label
	}
	return fmt.Sprintf("%s %d", label, index+1)
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
