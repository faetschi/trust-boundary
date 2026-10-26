// Command tbound-webchat runs the untrusted presentation gateway for the tbound
// Pi chat workspace.
//
// It is a loopback-only gateway. The demonstration modes (--demo, --fixture,
// --replay, --receipt, --receipt-demo) are GET-only, read-only projections. With
// --live and --socket it pairs one browser and forwards requests to the product
// service over the user's socket. In every mode it launches no Pi, executes no
// tool, and holds no policy, effect, lease or publication authority.
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

	"tbound/webchat/internal/webchat"
)

// defaultAddress is the literal loopback address bound when neither --listen nor --addr is given.
const defaultAddress = "127.0.0.1:8791"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "tbound-webchat:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	flags := flag.NewFlagSet("tbound-webchat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("addr", "", "literal loopback address to bind (alias of --listen)")
	listen := flags.String("listen", "", "literal loopback address to bind (default 127.0.0.1:8791)")
	demo := flags.Bool("demo", false, "serve the embedded non-claim-bearing development fixture")
	fixture := flags.String("fixture", "", "serve a development fixture document (non-claim-bearing)")
	replay := flags.String("replay", "", "serve retained observations from a replay document")
	receipt := flags.String("receipt", "", "import a retained post-run Pi receipt (development or production)")
	receiptDemo := flags.Bool("receipt-demo", false, "import the embedded synthetic example receipt")
	live := flags.Bool("live", false, "serve the live product chat: pair one browser, forward to the product service (needs --socket)")
	socket := flags.String("socket", "", "product service Unix socket for --live (for example /run/tbound-product/product.sock)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}

	selected := 0
	for _, enabled := range []bool{*demo, *fixture != "", *replay != "", *receipt != "", *receiptDemo, *live} {
		if enabled {
			selected++
		}
	}
	if selected > 1 {
		return errors.New("--demo, --fixture, --replay, --receipt, --receipt-demo and --live are mutually exclusive")
	}

	if *address != "" && *listen != "" {
		return errors.New("--addr and --listen are the same flag; pass one")
	}
	if *listen != "" {
		*address = *listen
	}
	if *address == "" {
		*address = defaultAddress
	}
	if *live != (*socket != "") {
		if *live {
			return errors.New("--live needs --socket PATH, the product service socket")
		}
		return errors.New("--socket is only used with --live")
	}
	if err := validateLoopbackAddress(*address); err != nil {
		return err
	}

	if !*demo && *fixture == "" && *replay == "" && *receipt == "" && !*receiptDemo && !*live {
		fmt.Fprintln(stderr, "note: no source selected; serving the unavailable projection (pass --demo for the labelled fixture)")
	}

	var (
		store *webchat.Store
		err   error
	)
	switch {
	case *live:
		// The live gateway holds no store; it needs the bound address, so it is built after listening.
	case *fixture != "":
		store, err = webchat.NewFixtureStoreFromPath(*fixture)
	case *replay != "":
		store, err = webchat.NewReplayStoreFromPath(*replay)
	case *receipt != "":
		store, err = webchat.NewReceiptStoreFromPath(*receipt)
	case *receiptDemo:
		store, err = webchat.NewReceiptDemoStore()
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

	// Registered before anything is printed, so a signal sent once the banner is out is
	// a shutdown, not the default kill.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, shutdownSignals()...)
	defer signal.Stop(stop)

	handler := http.Handler(nil)
	pairToken := ""
	if *live {
		gateway, token, err := webchat.NewLiveGateway(webchat.LiveOptions{Socket: *socket, BoundAddr: bound})
		if err != nil {
			listener.Close()
			return err
		}
		handler, pairToken = gateway.Handler(), token
	} else {
		handler = webchat.NewHandler(store, webchat.HandlerOptions{AllowedHost: bound, AllowedOrigin: "http://" + bound})
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// A read timeout bounds a slow/stalled request. The demonstration modes read no
		// body and close a body-bearing request without draining; the live gateway
		// reads at most 64 KiB, inside this timeout.
		ReadTimeout:    15 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 16 << 10,
		// No WriteTimeout is configured: SSE responses are long-lived and each
		// response write carries its own explicit deadline that covers the flush.
	}

	appErr := webchat.AppStatus()
	for _, line := range startupBanner(bound, appErr) {
		fmt.Fprintln(stdout, line)
	}
	if *live {
		for _, line := range liveBanner(bound, *socket, pairToken) {
			fmt.Fprintln(stdout, line)
		}
	} else {
		fmt.Fprintf(stdout, "source: %s (read-only projection; no launch, cancel, approve, or execute controls)\n", store.Snapshot().Source)
		fmt.Fprintln(stdout, "claim boundary: G1=false; H1 unexecuted; containment not-established; claim-bearing=false")
	}
	if appErr != nil {
		fmt.Fprintf(stderr, "note: /app/ is unavailable: %v (/ serves the legacy workspace as a fallback)\n", appErr)
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

	select {
	case serveErr := <-errCh:
		return serveErr
	case <-stop:
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

// liveBanner is what a live gateway prints. The pairing URL carries the one-time
// token in its fragment. It goes to stdout only: nothing here writes it to a file or
// a log, so do not keep a redirect of stdout.
func liveBanner(bound, socket, token string) []string {
	return []string{
		fmt.Sprintf("source: live (product service socket %s; requests run under your user; no policy, effect or publication authority here)", socket),
		fmt.Sprintf("pair this browser once, within %d minutes: http://%s/pair#%s", int(webchat.PairingLifetime/time.Minute), bound, token),
		"the pairing token works once; restart tbound-webchat to pair again",
	}
}

// startupBanner lists the URLs to open, with the React app first. When the app is
// unavailable, the first line says that / is serving the legacy fallback.
func startupBanner(bound string, appErr error) []string {
	ui := fmt.Sprintf("UI: http://%s/ (React app at /app/)", bound)
	if appErr != nil {
		ui = fmt.Sprintf("UI: http://%s/ (fallback: the React app is unavailable, so the legacy workspace is served here)", bound)
	}
	return []string{
		ui,
		fmt.Sprintf("legacy UI (deprecated; removed after sign-off): http://%s/legacy/", bound),
		fmt.Sprintf("tbound-webchat listening on http://%s", bound),
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
