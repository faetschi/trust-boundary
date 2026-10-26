//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
)

const (
	productServiceReadTimeout     = 5 * time.Second
	productServiceWriteTimeout    = 10 * time.Second
	productServiceMaxConnections  = 64
	productServiceObservePoll     = 100 * time.Millisecond
	productServiceExpiryInterval  = time.Second
	productServiceMaxListSessions = 2000
	productServiceRecentTurns     = 64
	productServiceSocketMode      = 0o660
	productServiceSocketDirMode   = 0o750
)

var (
	errProductServiceNoHost = errors.New("the product service has no host profile: the provider profile wiring, publication trust and " +
		"installed policy of a qualified product host are not configured")
	errProductServiceNoConversation = errors.New("the product service host configured no provider conversation factory")
	errProductServiceTurnsRunning   = errors.New("turns were still running when the shutdown grace period ended; restart reconciliation settles them")
)

// productServiceHost fills the trusted pieces of a product turn that no config file can name: the
// provider conversation factory (it receives each session's profile ID in its providerbridge.Config),
// the publication trust, the private Git pin, the installed gate policies with their digests, the Xattr
// attestation, the prompts and the worker step bound. The command uses newProductServiceHostProfile;
// tests may inject a fixture.
type productServiceHost func(env *productTurnEnv) error

// productServiceOptions are the injected parts of the service. Production sets Host and Log only.
type productServiceOptions struct {
	Host productServiceHost
	Log  io.Writer
	// ExtraRoots are test-only directories allowed besides the product defaults.
	ExtraRoots []string
	// Principal derives the caller's principal from its connection. Nil reads SO_PEERCRED.
	Principal func(*net.UnixConn) (productsession.Principal, error)
	// ReviewExpiryEvery overrides the review-expiry tick.
	ReviewExpiryEvery time.Duration
	// beforeListen is a test seam called after restart reconciliation and before the socket exists.
	beforeListen func(env *productTurnEnv)
}

// productService is the product service: it admits turns for authenticated principals, runs them in
// its own goroutines, and projects their durable records.
type productService struct {
	cfg        productServiceConfig
	catalog    *productsession.Catalog
	env        *productTurnEnv
	events     *productEventProjector
	listener   *net.UnixListener
	socketLock *os.File
	principal  func(*net.UnixConn) (productsession.Principal, error)
	log        *log.Logger

	ctx    context.Context // ends at shutdown: stops new admissions, long polls and the expiry ticker
	cancel context.CancelFunc
	slots  chan struct{}
	loops  sync.WaitGroup // accept loop and expiry ticker
	conns  sync.WaitGroup
	turns  sync.WaitGroup
	once   sync.Once
	result error
}

// startProductService verifies the registration, opens the catalog, reconciles unresolved turns of an
// earlier process and only then starts listening.
func startProductService(cfg productServiceConfig, opts productServiceOptions) (_ *productService, err error) {
	if err := cfg.validate(opts.ExtraRoots); err != nil {
		return nil, err
	}
	if opts.Host == nil {
		return nil, errProductServiceNoHost
	}
	registration, err := productsession.LoadRegistration(cfg.RegistrationPath, cfg.RegistrationSignaturePath, cfg.RegistrationPublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load the product registration: %w", err)
	}
	for _, project := range registration.Projects {
		if cfg.Projects[project.ProjectID] == "" {
			return nil, fmt.Errorf("registered project %q has no managed directory in the config", project.ProjectID)
		}
	}
	if len(cfg.Projects) != len(registration.Projects) {
		return nil, errors.New("the config names a project the registration does not")
	}
	catalog, err := productsession.Open(cfg.CatalogPath, registration)
	if err != nil {
		return nil, fmt.Errorf("open the product catalog: %w", err)
	}
	env := &productTurnEnv{Catalog: catalog, Registration: registration, StateRoot: cfg.StateRoot, ProjectRoots: map[string]string{}}
	for id, dir := range cfg.Projects {
		env.ProjectRoots[id] = dir
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, env.Close(), catalog.Close())
		}
	}()
	if err := opts.Host(env); err != nil {
		return nil, fmt.Errorf("configure the product host: %w", err)
	}
	if env.NewConversation == nil {
		return nil, errProductServiceNoConversation
	}
	logger := log.New(io.Discard, "", 0)
	if opts.Log != nil {
		logger = log.New(opts.Log, "product-serve: ", log.LstdFlags)
	}
	// Reconciliation records what it cannot prove as unknown turns, which is a settled state that
	// blocks only its own project. Only a turn that is still unresolved afterwards stops the start.
	if reconcileErr := env.ReconcileAfterRestart(context.Background()); reconcileErr != nil {
		logger.Printf("restart reconciliation: %v", reconcileErr)
	}
	if left := catalog.UnresolvedTurns(); len(left) != 0 {
		return nil, fmt.Errorf("restart reconciliation left %d turns unresolved", len(left))
	}
	if opts.beforeListen != nil {
		opts.beforeListen(env)
	}
	listener, socketLock, err := listenProductSocket(cfg.SocketPath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &productService{cfg: cfg, catalog: catalog, env: env, events: newProductEventProjector(env), listener: listener, socketLock: socketLock,
		principal: opts.Principal, log: logger, ctx: ctx, cancel: cancel, slots: make(chan struct{}, productServiceMaxConnections)}
	if s.principal == nil {
		s.principal = productPeerPrincipal
	}
	every := opts.ReviewExpiryEvery
	if every <= 0 {
		every = productServiceExpiryInterval
	}
	s.loops.Add(2)
	go s.acceptLoop()
	go s.expiryLoop(every)
	return s, nil
}

// listenProductSocket holds the socket's writer lock before replacing a stale socket. The caller
// keeps the lock file open for the service lifetime and never unlinks it.
func listenProductSocket(socketPath string) (_ *net.UnixListener, lock *os.File, err error) {
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, productServiceSocketDirMode); err != nil {
		return nil, nil, fmt.Errorf("create the socket directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&^productServiceSocketDirMode != 0 {
		return nil, nil, errors.New("the socket directory must be a directory of mode 0750 or stricter")
	}
	lock, err = os.OpenFile(socketPath+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open the socket lock: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, lock.Close())
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, lock, fmt.Errorf("lock the product socket: %w", err)
	}
	if existing, err := os.Lstat(socketPath); err == nil {
		if existing.Mode().Type() != os.ModeSocket {
			return nil, lock, errors.New("the socket path exists and is not a socket")
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, lock, fmt.Errorf("remove the stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, lock, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, lock, fmt.Errorf("listen on the product socket: %w", err)
	}
	if err := os.Chmod(socketPath, productServiceSocketMode); err != nil {
		_ = listener.Close()
		return nil, lock, fmt.Errorf("set the socket mode: %w", err)
	}
	return listener, lock, nil
}

// productPeerPrincipal is the connection's SO_PEERCRED UID. A payload never carries it.
func productPeerPrincipal(conn *net.UnixConn) (productsession.Principal, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *syscall.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, controlErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if controlErr != nil || credentials == nil || credentials.Pid <= 0 {
		return 0, errors.Join(errors.New("peer credentials are unavailable"), controlErr)
	}
	return productsession.Principal(credentials.Uid), nil
}

func (s *productService) acceptLoop() {
	defer s.loops.Done()
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Printf("accept: %v", err)
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		select {
		case s.slots <- struct{}{}:
			s.conns.Add(1)
			go func() {
				defer func() { <-s.slots; s.conns.Done() }()
				s.handle(conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (s *productService) expiryLoop(every time.Duration) {
	defer s.loops.Done()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.env.ExpireReviews(now); err != nil {
				s.log.Printf("review expiry: %v", err)
			}
		}
	}
}

// handle serves one connection: one request frame, one response frame.
func (s *productService) handle(conn *net.UnixConn) {
	defer conn.Close()
	principal, err := s.principal(conn)
	if err != nil {
		s.reply(conn, unavailable())
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(productServiceReadTimeout))
	var request productprotocol.Request
	if err := productprotocol.ReadFrame(conn, productprotocol.MaxRequestBytes, &request); err != nil {
		if errors.Is(err, productprotocol.ErrProtocol) {
			s.reply(conn, failure(productprotocol.CodeProtocolError))
		}
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if request.Validate() != nil {
		s.reply(conn, failure(productprotocol.CodeProtocolError))
		return
	}
	s.reply(conn, s.dispatch(principal, request))
}

func (s *productService) reply(conn *net.UnixConn, response *productprotocol.Response) {
	_ = conn.SetWriteDeadline(time.Now().Add(productServiceWriteTimeout))
	if err := productprotocol.WriteFrame(conn, response, productprotocol.MaxResponseBytes); errors.Is(err, productprotocol.ErrProtocol) {
		// The projection outgrew its frame. Nothing was written, so answer plainly instead of cutting it short.
		s.log.Printf("a response exceeded its frame bound")
		_ = productprotocol.WriteFrame(conn, unavailable(), productprotocol.MaxResponseBytes)
	}
}

func succeeded() *productprotocol.Response {
	return &productprotocol.Response{Version: productprotocol.Version, Status: productprotocol.StatusOK}
}

func failure(code string) *productprotocol.Response {
	return &productprotocol.Response{Version: productprotocol.Version, Status: productprotocol.StatusError,
		Error: &productprotocol.Error{Code: code}}
}

func unavailable() *productprotocol.Response { return failure(productprotocol.CodeServiceUnavailable) }

// errorResponse maps a catalog error to its stable code. Anything else is SERVICE_UNAVAILABLE with
// no detail: prompts, paths and internal errors never reach a client.
func (s *productService) errorResponse(op string, err error) *productprotocol.Response {
	var catalogErr *productsession.Error
	switch {
	case errors.As(err, &catalogErr):
		return failure(string(catalogErr.Code))
	case errors.Is(err, errProductReviewNotRetained):
		return failure(string(productsession.CodeReviewNotPending))
	}
	s.log.Printf("%s: %v", op, err)
	return unavailable()
}

func (s *productService) dispatch(p productsession.Principal, r productprotocol.Request) *productprotocol.Response {
	switch r.Op {
	case productprotocol.OpCreateSession:
		return s.createSession(p, *r.CreateSession)
	case productprotocol.OpSubmitTurn:
		return s.submitTurn(p, *r.SubmitTurn)
	case productprotocol.OpGetSession:
		return s.getSession(p, *r.GetSession)
	case productprotocol.OpListSessions:
		return s.listSessions(p)
	case productprotocol.OpObserve:
		return s.observe(p, *r.Observe)
	case productprotocol.OpGetCandidate:
		return s.getCandidate(p, *r.GetCandidate)
	case productprotocol.OpCancelTurn:
		return s.cancelTurn(p, *r.CancelTurn)
	case productprotocol.OpResolveTurn:
		return s.resolveTurn(p, *r.ResolveTurn)
	}
	return failure(productprotocol.CodeProtocolError)
}

func (s *productService) createSession(p productsession.Principal, r productprotocol.CreateSession) *productprotocol.Response {
	// Registered principals first, then the project: the catalog repeats both checks atomically.
	if _, err := s.catalog.SessionsOf(p); err != nil {
		return s.errorResponse(productprotocol.OpCreateSession, err)
	}
	if _, known := s.env.ProjectRoots[r.ProjectID]; !known {
		return failure(string(productsession.CodeUnknownProject))
	}
	measured, err := s.env.MeasureProjectBaseline(r.ProjectID)
	if err != nil {
		return s.errorResponse(productprotocol.OpCreateSession, err)
	}
	profile, budget := s.createDefaults(r)
	session, replayed, err := s.catalog.CreateSession(p, productsession.CreateSessionRequest{ProjectID: r.ProjectID,
		ProviderProfile: profile, PublicationMode: r.PublicationMode, Budget: budget,
		RequestKey: r.RequestKey, ReadOnly: r.ReadOnly}, measured)
	if err != nil {
		return s.errorResponse(productprotocol.OpCreateSession, err)
	}
	response := succeeded()
	response.Session, response.Replayed = s.sessionView(session, false), replayed
	return response
}

// createDefaults applies the create_session defaults before the catalog sees the request: an empty
// provider profile is the registration's first profile, and a budget field of 0 is the registration's
// session limit for that field. A negative field stays as given, so the catalog refuses it.
func (s *productService) createDefaults(r productprotocol.CreateSession) (string, productsession.Budget) {
	profile, budget := r.ProviderProfile, productsession.Budget(r.Budget)
	if profile == "" {
		profile = s.env.Registration.ProviderProfiles[0]
	}
	limit := s.env.Registration.Limits.Session
	for _, field := range []struct {
		value *int64
		limit int64
	}{
		{&budget.HostCalls, limit.HostCalls}, {&budget.Tokens, limit.Tokens}, {&budget.DurationSeconds, limit.DurationSeconds},
	} {
		if *field.value == 0 {
			*field.value = field.limit
		}
	}
	return profile, budget
}

func (s *productService) submitTurn(p productsession.Principal, r productprotocol.SubmitTurn) *productprotocol.Response {
	session, err := s.catalog.Session(p, r.SessionID)
	if err != nil {
		return s.errorResponse(productprotocol.OpSubmitTurn, err)
	}
	if s.ctx.Err() != nil {
		return unavailable() // shutting down: no new admissions
	}
	measured, err := s.env.MeasureProjectBaseline(session.ProjectID)
	if err != nil {
		return s.errorResponse(productprotocol.OpSubmitTurn, err)
	}
	admission, err := s.catalog.SubmitTurn(p, productsession.SubmitTurnRequest{SessionID: r.SessionID,
		ExpectedRevision: r.ExpectedRevision, RequestKey: r.RequestKey, Prompt: r.Prompt,
		ExpectedBaselineDigest: r.ExpectedBaselineDigest}, measured)
	if err != nil {
		return s.errorResponse(productprotocol.OpSubmitTurn, err)
	}
	if !admission.Replayed {
		s.startTurn(admission)
	}
	response := succeeded()
	view := turnView(admission.Turn)
	response.Turn, response.Replayed = &view, admission.Replayed
	return response
}

// startTurn runs an admitted turn in its own goroutine. The turn does not depend on the connection or
// on the service context: a client that leaves keeps its accepted work running, and a shutdown waits
// for it instead of cancelling it.
func (s *productService) startTurn(admission productsession.Admission) {
	s.turns.Add(1)
	go func() {
		defer s.turns.Done()
		if _, err := s.env.RunTurn(context.Background(), admission); err != nil {
			s.log.Printf("turn %s ended with an error: %v", admission.Turn.ID, err)
		}
	}()
}

func (s *productService) getSession(p productsession.Principal, r productprotocol.SessionRef) *productprotocol.Response {
	session, err := s.catalog.Session(p, r.SessionID)
	if err != nil {
		return s.errorResponse(productprotocol.OpGetSession, err)
	}
	response := succeeded()
	response.Session = s.sessionView(session, true)
	return response
}

func (s *productService) listSessions(p productsession.Principal) *productprotocol.Response {
	sessions, err := s.catalog.SessionsOf(p)
	if err != nil {
		return s.errorResponse(productprotocol.OpListSessions, err)
	}
	response := succeeded()
	// shortcut: the list is the first 2000 sessions by ID, without turns; page it when a principal can own more.
	for _, session := range sessions[:min(len(sessions), productServiceMaxListSessions)] {
		response.Sessions = append(response.Sessions, *s.sessionView(session, false))
	}
	return response
}

func (s *productService) observe(p productsession.Principal, r productprotocol.Observe) *productprotocol.Response {
	deadline := time.Now().Add(time.Duration(r.WaitMillis) * time.Millisecond)
	for {
		session, err := s.catalog.Session(p, r.SessionID)
		if err != nil {
			return s.errorResponse(productprotocol.OpObserve, err)
		}
		events, err := s.events.session(session)
		if err != nil {
			return s.errorResponse(productprotocol.OpObserve, err)
		}
		if r.Cursor > uint64(len(events)) {
			return failure(string(productsession.CodeInvalidRequest))
		}
		if r.Cursor < uint64(len(events)) || !time.Now().Before(deadline) || s.ctx.Err() != nil {
			return observeWindow(events, r.Cursor)
		}
		select {
		case <-s.ctx.Done():
		case <-time.After(min(productServiceObservePoll, time.Until(deadline))):
		}
	}
}

// observeWindow answers with the events after cursor, at most MaxEvents and about
// productObserveBudget bytes, and the cursor to continue from.
func observeWindow(events []productprotocol.Event, cursor uint64) *productprotocol.Response {
	response := succeeded()
	response.NextCursor = cursor
	size := 0
	for _, event := range events[cursor:] {
		encoded, err := jsonSize(event)
		if err != nil || len(response.Events) == productprotocol.MaxEvents ||
			(len(response.Events) != 0 && size+encoded > productObserveBudget) {
			break
		}
		size += encoded
		response.Events = append(response.Events, event)
		response.NextCursor++
	}
	return response
}

func (s *productService) getCandidate(p productsession.Principal, r productprotocol.TurnRef) *productprotocol.Response {
	turn, err := s.sessionTurn(p, r.SessionID, r.TurnID)
	if err != nil {
		return s.errorResponse(productprotocol.OpGetCandidate, err)
	}
	store, held := s.env.pendingReviewStore(turn.ID)
	if turn.State != productsession.StateAwaitingReview || !held {
		return failure(string(productsession.CodeReviewNotPending))
	}
	view, err := buildProductCandidate(turn.ID, store, turn.CandidateDigest)
	if err != nil {
		return s.errorResponse(productprotocol.OpGetCandidate, err)
	}
	response := succeeded()
	response.Candidate = &view
	return response
}

func (s *productService) cancelTurn(p productsession.Principal, r productprotocol.CancelTurn) *productprotocol.Response {
	if _, err := s.sessionTurn(p, r.SessionID, r.TurnID); err != nil {
		return s.errorResponse(productprotocol.OpCancelTurn, err)
	}
	_, err := s.env.CancelTurn(context.Background(), p, productsession.CancelTurnRequest{SessionID: r.SessionID, TurnID: r.TurnID,
		ExpectedRevision: r.ExpectedRevision, RequestKey: r.RequestKey})
	turn, _ := s.catalog.Turn(r.TurnID)
	// A request the catalog recorded stands even when ending the turn afterwards failed: the turn reports its real state.
	if err != nil && (catalogRefusal(err) || !turn.CancelRequested) {
		return s.errorResponse(productprotocol.OpCancelTurn, err)
	}
	if err != nil {
		s.log.Printf("%s: %v", productprotocol.OpCancelTurn, err)
	}
	return s.turnResponse(turn)
}

func (s *productService) resolveTurn(p productsession.Principal, r productprotocol.ResolveTurn) *productprotocol.Response {
	if _, err := s.sessionTurn(p, r.SessionID, r.TurnID); err != nil {
		return s.errorResponse(productprotocol.OpResolveTurn, err)
	}
	_, err := s.env.ResolveTurn(context.Background(), p, productsession.ResolveTurnRequest{SessionID: r.SessionID, TurnID: r.TurnID,
		ExpectedRevision: r.ExpectedRevision, RequestKey: r.RequestKey, CandidateDigest: r.CandidateDigest, Decision: r.Decision})
	turn, _ := s.catalog.Turn(r.TurnID)
	// A decision the catalog recorded stands even when the publication then failed or ended unknown: the
	// settled turn reports that disposition.
	if err != nil && (catalogRefusal(err) || turn.Resolution == "") {
		return s.errorResponse(productprotocol.OpResolveTurn, err)
	}
	if err != nil {
		s.log.Printf("%s: %v", productprotocol.OpResolveTurn, err)
	}
	return s.turnResponse(turn)
}

// catalogRefusal reports an error the catalog returned itself for the request, which recorded nothing,
// as opposed to a failure of the driver after the request was recorded.
func catalogRefusal(err error) bool {
	_, direct := err.(*productsession.Error)
	return direct
}

func (s *productService) turnResponse(turn productsession.Turn) *productprotocol.Response {
	response := succeeded()
	view := turnView(turn)
	response.Turn = &view
	return response
}

// sessionTurn authorizes the principal through the session, then finds the turn inside it.
func (s *productService) sessionTurn(p productsession.Principal, sessionID, turnID string) (productsession.Turn, error) {
	session, err := s.catalog.Session(p, sessionID)
	if err != nil {
		return productsession.Turn{}, err
	}
	turn, ok := s.catalog.Turn(turnID)
	if !ok || turn.SessionID != session.ID {
		return productsession.Turn{}, productsession.ErrCode(productsession.CodeUnknownTurn)
	}
	return turn, nil
}

func (s *productService) sessionView(session productsession.Session, withTurns bool) *productprotocol.SessionView {
	view := &productprotocol.SessionView{ID: session.ID, ProjectID: session.ProjectID, ProviderProfile: session.ProviderProfile,
		PublicationMode: session.PublicationMode, ReadOnly: session.ReadOnly, Revision: session.Revision, Blocked: session.Blocked,
		Budget: productprotocol.Budget(session.Budget), Consumed: productprotocol.Budget(session.Consumed),
		Reserved: productprotocol.Budget(session.Reserved), TotalTurns: len(session.Turns)}
	if withTurns {
		for i, id := range session.Turns {
			if turn, ok := s.catalog.Turn(id); ok {
				if i >= len(session.Turns)-productServiceRecentTurns || turn.State == productsession.StateAdmitted ||
					turn.State == productsession.StateLinked || turn.State == productsession.StateAwaitingReview || turn.State == productsession.StateUnknown {
					view.Turns = append(view.Turns, turnView(turn))
				}
			}
		}
	}
	view.TurnsTruncated = len(view.Turns) < view.TotalTurns
	return view
}

func turnView(turn productsession.Turn) productprotocol.TurnView {
	return productprotocol.TurnView{ID: turn.ID, Ordinal: turn.Ordinal, State: string(turn.State),
		Prompt: boundText(turn.Prompt, productViewPromptBytes), Outcome: turn.Outcome, TerminalReason: turn.TerminalReason,
		CancelRequested: turn.CancelRequested, Resolution: turn.Resolution, CandidateDigest: turn.CandidateDigest,
		ReviewTimeoutSeconds: turn.ReviewTimeoutSeconds}
}

// Shutdown stops accepting, lets in-flight requests and running turns finish within the grace period
// without cancelling them, then closes pending reviews and the catalog. A pending review is
// discarded with reason restart by the next start. It is safe to call more than once.
func (s *productService) Shutdown() error {
	s.once.Do(func() {
		s.cancel()
		s.result = errors.Join(s.listener.Close())
		s.loops.Wait()
		grace := time.Duration(s.cfg.grace()) * time.Second
		stillBusy := !waitWithin(&s.conns, grace) || !waitWithin(&s.turns, grace)
		if stillBusy {
			s.result = errors.Join(s.result, errProductServiceTurnsRunning)
			return
		}
		s.result = errors.Join(s.result, s.env.Close(), s.catalog.Close(), s.socketLock.Close())
	})
	return s.result
}

func waitWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// runProductServe is `tbound product-serve --config PATH`.
func runProductServe(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("product-serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path of the tbound-product-service-config/v1 file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("usage: tbound product-serve --config PATH")
	}
	cfg, err := loadProductServiceConfig(*configPath)
	if err != nil {
		return err
	}
	service, err := startProductService(cfg, productServiceOptions{Host: newProductServiceHostProfile(cfg), Log: stderr})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(stdout, "product service listening")
	<-ctx.Done()
	return service.Shutdown()
}

func jsonSize(v any) (int, error) {
	encoded, err := json.Marshal(v)
	return len(encoded), err
}
