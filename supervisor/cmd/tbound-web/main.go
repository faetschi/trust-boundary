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
	var journals, transcripts pathList
	flags.Var(&journals, "journal", "read-only tbound audit JSONL path (repeatable or comma-separated)")
	flags.Var(&transcripts, "transcript", "read-only supervisor transcript NDJSON path (repeatable or comma-separated)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !loopbackAddress(*address) {
		return errors.New("tbound-web has no remote authentication; --addr must use a loopback host")
	}
	sources := make([]webview.Source, 0, len(journals)+len(transcripts))
	for index, path := range journals {
		sources = append(sources, webview.Source{Name: sourceName("audit journal", index, len(journals)), Path: path, Kind: "audit"})
	}
	for index, path := range transcripts {
		sources = append(sources, webview.Source{Name: sourceName("supervisor transcript", index, len(transcripts)), Path: path, Kind: "transcript"})
	}
	if len(sources) == 0 {
		return errors.New("at least one --journal or --transcript path is required")
	}

	buffer := webview.NewBuffer(webview.DefaultBufferCapacity)
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
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
