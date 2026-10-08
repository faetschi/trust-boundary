// Command tbound-governance serves the read-only governance companion for the
// actual native Pi TUI. It never launches Pi, accepts chat, stops sessions,
// approves effects, runs tests, publishes files, or exposes source paths.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"tbound/supervisor/internal/governanceview"
)

const defaultAddress = "127.0.0.1:8790"

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-governance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("addr", defaultAddress, "literal loopback HTTP listen address")
	fixture := flags.String("fixture", "", "explicit sanitized governance fixture document")
	replay := flags.String("replay", "", "explicit retained governance replay document")
	var journals, transcripts, testJSON pathList
	flags.Var(&journals, "journal", "registered read-only audit JSONL source for live diagnostic mapping (repeatable or comma-separated)")
	flags.Var(&transcripts, "transcript", "registered read-only transcript JSONL source for live diagnostic mapping (repeatable or comma-separated)")
	flags.Var(&testJSON, "test-json", "registered read-only Pi project-test JSONL source for live diagnostic mapping (repeatable or comma-separated)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !loopbackAddress(*address) {
		return errors.New("tbound-governance has no remote authentication; --addr must use a literal loopback IP address")
	}
	if *fixture != "" && *replay != "" {
		return errors.New("--fixture and --replay are mutually exclusive")
	}
	if (*fixture != "" || *replay != "") && (len(journals) != 0 || len(transcripts) != 0 || len(testJSON) != 0) {
		return errors.New("fixture/replay input cannot be combined with live source paths")
	}
	if len(journals)+len(transcripts)+len(testJSON) != 0 {
		return errors.New("registered live file sources are refused until a no-follow, descriptor-bound tailer is available; use explicit fixture or replay input")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var store *governanceview.Store
	var err error
	switch {
	case *fixture != "":
		snapshot, loadErr := governanceview.LoadFixture(*fixture)
		if loadErr != nil {
			return loadErr
		}
		store, err = governanceview.NewStore(snapshot, governanceview.DefaultRecordCapacity)
	case *replay != "":
		snapshot, loadErr := governanceview.LoadReplay(*replay)
		if loadErr != nil {
			return loadErr
		}
		store, err = governanceview.NewStore(snapshot, governanceview.DefaultRecordCapacity)
	default:
		store, err = governanceview.NewStore(governanceview.NewUnavailableSnapshot(), governanceview.DefaultRecordCapacity)
	}
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		stop()
		return fmt.Errorf("listen on %s: %w", *address, err)
	}
	defer listener.Close()
	boundAddress := listener.Addr().String()
	server := &http.Server{
		Handler:           governanceview.NewHandler(store, governanceview.HandlerOptions{AllowedHost: boundAddress, AllowedOrigin: "http://" + boundAddress}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	writeStartupBanner(stderr, boundAddress, store.Snapshot().Mode, len(journals)+len(transcripts)+len(testJSON))

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		return shutdownErr
	case err := <-serverDone:
		stop()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func writeStartupBanner(writer io.Writer, address string, mode governanceview.Mode, sourceCount int) {
	fmt.Fprintln(writer, "tbound-governance - bounded, read-only native-Pi companion")
	fmt.Fprintf(writer, "mode %s; registered source count %d\n", mode, sourceCount)
	fmt.Fprintf(writer, "listening at http://%s/ - open this URL in a browser\n", address)
	fmt.Fprintln(writer, "no launch, stop, approval, execution, provider, publication, or arbitrary-path controls are exposed")
}

type pathList []string

func (paths *pathList) String() string { return strings.Join(*paths, ",") }

func (paths *pathList) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return errors.New("registered source paths must not be empty")
		}
		*paths = append(*paths, part)
	}
	return nil
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
