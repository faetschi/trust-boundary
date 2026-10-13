// Command tbound-webchat runs the untrusted presentation gateway for the tbound
// Pi chat workspace.
//
// It is a loopback-only, GET-only, read-only projection server. It does not
// launch Pi, execute a tool, hold policy, admit an effect, or write the live
// workspace. It exposes no control route and accepts no request body. A live
// projection requires the supervisor-owned session/stream service, which does
// not exist yet; requesting it fails closed instead of fabricating a source.
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
	"time"

	"tbound/supervisor/internal/webchat"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "tbound-webchat:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	flags := flag.NewFlagSet("tbound-webchat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("addr", "127.0.0.1:8791", "literal loopback address to bind")
	demo := flags.Bool("demo", false, "serve the embedded non-claim-bearing development fixture")
	fixture := flags.String("fixture", "", "serve a development fixture document (non-claim-bearing)")
	replay := flags.String("replay", "", "serve retained observations from a replay document")
	live := flags.Bool("live", false, "serve a live projection (requires the supervisor session/stream service; not implemented)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}

	selected := 0
	for _, enabled := range []bool{*demo, *fixture != "", *replay != "", *live} {
		if enabled {
			selected++
		}
	}
	if selected > 1 {
		return errors.New("--demo, --fixture, --replay and --live are mutually exclusive")
	}

	if err := validateLoopbackAddress(*address); err != nil {
		return err
	}

	if !*demo && *fixture == "" && *replay == "" && !*live {
		fmt.Fprintln(stderr, "note: no source selected; serving the unavailable projection (pass --demo for the labelled fixture)")
	}

	var (
		store *webchat.Store
		err   error
	)
	switch {
	case *live:
		store, err = webchat.NewLiveStore()
		if err != nil {
			return fmt.Errorf("live projection refused: %w", err)
		}
	case *fixture != "":
		store, err = webchat.NewFixtureStoreFromPath(*fixture)
	case *replay != "":
		store, err = webchat.NewReplayStoreFromPath(*replay)
	case *demo:
		store, err = webchat.NewFixtureStore()
	default:
		store, err = webchat.NewUnavailableStore()
	}
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *address, err)
	}
	bound := listener.Addr().String()

	server := &http.Server{
		Handler:           webchat.NewHandler(store, webchat.HandlerOptions{AllowedHost: bound, AllowedOrigin: "http://" + bound}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// No ReadTimeout or WriteTimeout is configured. Request bodies are
		// rejected outright, and each response write is bounded with an
		// explicit deadline that also covers the flush.
	}

	source := store.Snapshot().Source
	fmt.Fprintf(stdout, "tbound-webchat listening on http://%s\n", bound)
	fmt.Fprintf(stdout, "source: %s (read-only projection; no launch, cancel, approve, or execute controls)\n", source)
	if source != webchat.SourceLive {
		fmt.Fprintln(stdout, "claim boundary: G1=false; H1 unexecuted; containment not-established; claim-bearing=false")
	}

	errCh := make(chan error, 1)
	go func() {
		serveErr := server.Serve(listener)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
			return
		}
		errCh <- nil
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, shutdownSignals()...)
	select {
	case serveErr := <-errCh:
		return serveErr
	case <-stop:
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("address must be host:port: %w", err)
	}
	if port == "" {
		return errors.New("address must include a port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("address %q must be a literal loopback IP (for example 127.0.0.1)", address)
	}
	return nil
}
