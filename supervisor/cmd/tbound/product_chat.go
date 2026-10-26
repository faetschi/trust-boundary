package main

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
)

const chatUsage = `usage: tbound chat [--socket PATH] --project ID [--provider PROFILE] [--mode review|auto]
                   [--read-only] [--budget-host-calls N] [--budget-tokens N] [--budget-seconds N]
       tbound chat [--socket PATH] --session ID

  --project ID     create a session on a registered project
  --provider NAME  provider profile (default: the service's choice)
  --mode MODE      review (default) holds changes for your approval; auto publishes them
  --read-only      no tool can change the project
  --budget-*       session budget (default: the service's choice)
  --session ID     reconnect to your session: replays its history and resubmits nothing
  --socket PATH    service socket (default ` + productsession.DefaultServiceSocketPath + `)

Each line you type is a prompt. Commands: /cancel /publish /discard /status /quit
Ctrl-C once cancels the running turn; a second Ctrl-C, Ctrl-C when idle, /quit and end of input
detach without cancelling anything. Accepted work keeps running in the service.
Exit status: 0 normal, 2 refused, 3 conflict or stale state, 4 unresolved or unknown outcome, 1 other.
`

const chatObserveWait = 20 * time.Second

// errChatDetach unwinds the client without cancelling anything.
var errChatDetach = errors.New("detached")

// chatEnv is everything the client takes from its process, so tests can supply their own.
type chatEnv struct {
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	interrupts  <-chan os.Signal
	live        bool // stdin is a terminal: lines typed during a turn act at once; piped lines wait
	observeWait time.Duration
	attempts    int
	backoff     time.Duration
}

func runChat(args []string, stdout, stderr io.Writer) error {
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	info, err := os.Stdin.Stat()
	live := err == nil && info.Mode()&os.ModeCharDevice != 0
	return chatMain(chatEnv{stdin: os.Stdin, stdout: stdout, stderr: stderr, interrupts: interrupts, live: live}, args)
}

type chatOptions struct {
	socket  string
	session string
	create  productprotocol.CreateSession
}

func parseChatFlags(stderr io.Writer, args []string) (chatOptions, error) {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, chatUsage) }
	var o chatOptions
	fs.StringVar(&o.socket, "socket", productsession.DefaultServiceSocketPath, "")
	fs.StringVar(&o.session, "session", "", "")
	fs.StringVar(&o.create.ProjectID, "project", "", "")
	fs.StringVar(&o.create.ProviderProfile, "provider", "", "")
	fs.StringVar(&o.create.PublicationMode, "mode", chatModeReview, "")
	fs.BoolVar(&o.create.ReadOnly, "read-only", false, "")
	fs.Int64Var(&o.create.Budget.HostCalls, "budget-host-calls", 0, "")
	fs.Int64Var(&o.create.Budget.Tokens, "budget-tokens", 0, "")
	fs.Int64Var(&o.create.Budget.DurationSeconds, "budget-seconds", 0, "")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	createFlags := false
	fs.Visit(func(f *flag.Flag) {
		createFlags = createFlags || (f.Name != "socket" && f.Name != "session" && f.Name != "project")
	})
	b := o.create.Budget
	switch {
	case fs.NArg() != 0:
		return o, fmt.Errorf("chat: unexpected arguments %q", fs.Args())
	case (o.session == "") == (o.create.ProjectID == ""):
		return o, errors.New("chat: give exactly one of --project or --session")
	case o.session != "" && createFlags:
		return o, errors.New("chat: --session takes no session settings")
	case o.create.PublicationMode != chatModeReview && o.create.PublicationMode != chatModeAuto:
		return o, errors.New("chat: --mode must be review or auto")
	case b.HostCalls < 0 || b.Tokens < 0 || b.DurationSeconds < 0:
		return o, errors.New("chat: budgets cannot be negative")
	}
	return o, nil
}

// chatSession is one attached conversation. Its methods print what the user needs and record in
// status the exit status of the last command; only a detach unwinds as an error.
type chatSession struct {
	chatEnv
	cl     *chatClient
	lines  <-chan string
	id     string
	cursor uint64          // next event to show; observations replay from 0 on every attach
	shown  map[string]bool // candidate digests whose diff was printed
	status int
}

func chatMain(env chatEnv, args []string) error {
	opts, err := parseChatFlags(env.stderr, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	s := &chatSession{chatEnv: env, shown: map[string]bool{}, lines: chatReadLines(env.stdin)}
	s.observeWait = cmp.Or(env.observeWait, chatObserveWait)
	s.cl = &chatClient{socket: opts.socket, attempts: cmp.Or(env.attempts, chatDefaultAttempt), backoff: cmp.Or(env.backoff, chatDefaultBackoff)}
	ctx := context.Background()
	if opts.session != "" {
		err = s.reconnect(ctx, opts.session)
	} else {
		err = s.create(ctx, opts.create)
	}
	if err == nil {
		err = s.repl(ctx)
	}
	if err != nil && !errors.Is(err, errChatDetach) {
		code, text := chatDescribe(err)
		fmt.Fprintf(s.stderr, "%s: %s\n", chatExitNames[code], text)
		return chatExit(code)
	}
	if s.id != "" {
		fmt.Fprintf(s.stdout, "detached from %s; accepted work keeps running. Reconnect with: tbound chat --session %s\n", chatLine(s.id), chatLine(s.id))
	}
	return chatExit(s.status)
}

func chatReadLines(r io.Reader) <-chan string {
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines
}

func chatNewKey() string {
	var b [16]byte
	rand.Read(b[:])
	return "ck-" + hex.EncodeToString(b[:])
}

func (s *chatSession) call(ctx context.Context, req productprotocol.Request) (*productprotocol.Response, error) {
	return s.cl.call(ctx, req, chatCallTimeout)
}

func (s *chatSession) refresh(ctx context.Context) (*productprotocol.SessionView, error) {
	resp, err := s.call(ctx, productprotocol.Request{Op: productprotocol.OpGetSession, GetSession: &productprotocol.SessionRef{SessionID: s.id}})
	if err != nil {
		return nil, err
	}
	if resp.Session == nil {
		return nil, &chatServiceError{Code: "PROTOCOL_ERROR", Detail: "response has no session"}
	}
	return resp.Session, nil
}

// fail shows err, records its exit status, and after a stale revision shows the session as it is
// now. It never retries or resubmits. A detach passes through.
func (s *chatSession) fail(ctx context.Context, err error) error {
	if errors.Is(err, errChatDetach) {
		return err
	}
	code, text := chatDescribe(err)
	s.status = code
	fmt.Fprintf(s.stderr, "%s: %s\n", chatExitNames[code], text)
	if chatIsCode(err, string(productsession.CodeStaleRevision)) {
		if view, rerr := s.refresh(ctx); rerr == nil {
			printSession(s.stdout, view, true)
		}
	}
	return nil
}

func (s *chatSession) create(ctx context.Context, req productprotocol.CreateSession) error {
	req.RequestKey = chatNewKey()
	resp, err := s.call(ctx, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &req})
	if err != nil {
		return err
	}
	if resp.Session == nil || resp.Session.ID == "" {
		return &chatServiceError{Code: "PROTOCOL_ERROR", Detail: "response has no session"}
	}
	s.id = resp.Session.ID
	printSession(s.stdout, resp.Session, false)
	fmt.Fprintf(s.stdout, "reconnect later with: tbound chat --session %s\n", chatLine(s.id))
	return nil
}

// reconnect replays the session's observations from the start, then follows an unresolved turn.
// It never submits anything.
func (s *chatSession) reconnect(ctx context.Context, id string) error {
	s.id = id
	view, err := s.refresh(ctx)
	if err != nil {
		return err
	}
	printSession(s.stdout, view, false)
	if err := s.replay(ctx); err != nil {
		return err
	}
	view, err = s.refresh(ctx)
	if err != nil {
		return err
	}
	t := chatPending(view, chatStateAdmitted, chatStateLinked, chatStateAwaitingReview, chatStateUnknown)
	switch {
	case t == nil:
		return nil
	case t.State == chatStateUnknown:
		s.status = chatExitUnknown
		fmt.Fprintf(s.stderr, "turn-%d has an unknown outcome; an operator must reconcile it\n", t.Ordinal)
		return nil
	case t.State == chatStateAwaitingReview && t.Resolution == "" && !t.CancelRequested:
		return s.review(ctx, t.ID, "")
	}
	fmt.Fprintf(s.stdout, "turn-%d is %s; following it\n", t.Ordinal, chatLine(t.State))
	return s.settle(ctx, t.ID, !t.CancelRequested && t.State != chatStateAwaitingReview)
}

func (s *chatSession) repl(ctx context.Context) error {
	for {
		if s.live {
			fmt.Fprint(s.stdout, "> ")
		}
		line, err := s.readLine()
		if err != nil {
			return nil // end of input or an idle Ctrl-C: detach
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case line == "/quit":
			return nil
		case line == "/status":
			err = s.showStatus(ctx)
		case line == "/cancel":
			err = s.cancelPending(ctx)
		case line == "/publish":
			err = s.resolvePending(ctx, chatDecisionPublish)
		case line == "/discard":
			err = s.resolvePending(ctx, chatDecisionDiscard)
		case strings.HasPrefix(line, "/"):
			fmt.Fprintf(s.stderr, "unknown command %s (commands: /cancel /publish /discard /status /quit)\n", chatLine(line))
		default:
			err = s.submit(ctx, line)
		}
		if err != nil {
			return err
		}
	}
}

func (s *chatSession) readLine() (string, error) {
	select {
	case line, ok := <-s.lines:
		if !ok {
			return "", errChatDetach
		}
		return line, nil
	case <-s.interrupts:
		return "", errChatDetach
	}
}

func (s *chatSession) showStatus(ctx context.Context) error {
	view, err := s.refresh(ctx)
	if err != nil {
		return s.fail(ctx, err)
	}
	printSession(s.stdout, view, true)
	return nil
}

// submit sends one prompt against the revision just read, under a fresh request key. A transport
// failure is retried by the client with that same key. Any other failure, a stale revision
// included, is reported and the prompt is not sent again.
func (s *chatSession) submit(ctx context.Context, prompt string) error {
	view, err := s.refresh(ctx)
	if err != nil {
		return s.fail(ctx, err)
	}
	resp, err := s.call(ctx, productprotocol.Request{Op: productprotocol.OpSubmitTurn, SubmitTurn: &productprotocol.SubmitTurn{
		SessionID: s.id, ExpectedRevision: view.Revision, RequestKey: chatNewKey(), Prompt: prompt}})
	if err != nil {
		return s.fail(ctx, err)
	}
	if resp.Turn == nil {
		return s.fail(ctx, &chatServiceError{Code: "PROTOCOL_ERROR", Detail: "response has no turn"})
	}
	if resp.Replayed {
		fmt.Fprintln(s.stdout, "the service had already accepted this prompt; following that turn")
	}
	return s.settle(ctx, resp.Turn.ID, true)
}

// settle follows a turn until it is terminal or, when running, awaits review.
func (s *chatSession) settle(ctx context.Context, turnID string, running bool) error {
	end, err := s.watch(ctx, turnID, running)
	if err != nil {
		return s.fail(ctx, err)
	}
	if end.review {
		return s.review(ctx, turnID, "")
	}
	s.status = chatOutcomeExit(end.outcome, end.reason)
	return nil
}

type chatEnd struct {
	review          bool
	outcome, reason string
}

type chatObserved struct {
	resp *productprotocol.Response
	err  error
}

// watch observes until the turn settles. While running, the first interrupt (or /cancel on a
// terminal) requests cancellation and keeps observing. The second interrupt, /quit, or the end of
// terminal input detaches. When not running (cancel or a review decision is already recorded) an
// interrupt detaches at once.
func (s *chatSession) watch(ctx context.Context, turnID string, running bool) (chatEnd, error) {
	cancelSent, reviewSettles := !running, running
	liveLines := s.lines
	if !s.live {
		liveLines = nil
	}
	requestCancel := func() {
		cancelSent, reviewSettles = true, false
		s.cancelTurn(ctx, turnID)
	}
	for {
		done := make(chan chatObserved, 1)
		octx, stop := context.WithCancel(ctx)
		req := chatObserve(s.id, s.cursor, s.observeWait)
		go func() {
			resp, err := s.cl.call(octx, req, s.observeWait+chatObserveMargin)
			done <- chatObserved{resp, err}
		}()
		var got chatObserved
	wait:
		for {
			select {
			case got = <-done:
				break wait
			case <-s.interrupts:
				if cancelSent {
					stop()
					return chatEnd{}, errChatDetach
				}
				requestCancel()
			case line, ok := <-liveLines:
				if !ok {
					stop()
					return chatEnd{}, errChatDetach
				}
				switch strings.TrimSpace(line) {
				case "/quit":
					stop()
					return chatEnd{}, errChatDetach
				case "/cancel":
					if cancelSent {
						fmt.Fprintln(s.stderr, "cancellation was already requested")
					} else {
						requestCancel()
					}
				case "/status":
					s.showStatus(ctx)
				default:
					fmt.Fprintln(s.stderr, "a turn is running: /cancel, /status, /quit or Ctrl-C")
				}
			}
		}
		stop()
		if got.err != nil {
			return chatEnd{}, got.err
		}
		if end := s.absorb(got.resp, turnID, reviewSettles); end != nil {
			return *end, nil
		}
		if len(got.resp.Events) > 0 && !got.resp.Gap {
			continue
		}
		// A quiet wait or a gap: the session state decides, in case a settling event was skipped.
		view, err := s.refresh(ctx)
		if err != nil {
			return chatEnd{}, err
		}
		if end := chatSettled(chatFindTurn(view, turnID), reviewSettles); end != nil {
			if err := s.replay(ctx); err != nil {
				return chatEnd{}, err
			}
			return *end, nil
		}
	}
}

func chatObserve(id string, cursor uint64, wait time.Duration) productprotocol.Request {
	return productprotocol.Request{Op: productprotocol.OpObserve, Observe: &productprotocol.Observe{
		SessionID: id, Cursor: cursor, WaitMillis: wait.Milliseconds()}}
}

// absorb prints the events not yet shown, advances the cursor, and reports how turnID settled, if
// these events settle it. A terminal event outranks an awaiting-review event in the same batch.
func (s *chatSession) absorb(resp *productprotocol.Response, turnID string, reviewSettles bool) *chatEnd {
	var end *chatEnd
	for _, ev := range resp.Events {
		if ev.Seq < s.cursor {
			continue
		}
		printEvent(s.stdout, ev)
		s.cursor = ev.Seq + 1
		if ev.TurnID != turnID {
			continue
		}
		switch ev.Kind {
		case productprotocol.EventTurnTerminal:
			end = &chatEnd{outcome: ev.Outcome, reason: ev.Reason}
		case productprotocol.EventTurnAwaitingReview:
			if reviewSettles && end == nil {
				end = &chatEnd{review: true}
			}
		}
	}
	if resp.Gap {
		fmt.Fprintln(s.stderr, "note: the service no longer has some earlier events")
	}
	s.cursor = max(s.cursor, resp.NextCursor)
	return end
}

// replay shows every event after the cursor without waiting.
func (s *chatSession) replay(ctx context.Context) error {
	for {
		resp, err := s.call(ctx, chatObserve(s.id, s.cursor, 0))
		if err != nil {
			return err
		}
		before := s.cursor
		s.absorb(resp, "", false)
		if s.cursor == before {
			return nil
		}
	}
}

func chatFindTurn(view *productprotocol.SessionView, id string) *productprotocol.TurnView {
	for i := range view.Turns {
		if view.Turns[i].ID == id {
			return &view.Turns[i]
		}
	}
	return nil
}

// chatPending is the last turn in one of the given states.
func chatPending(view *productprotocol.SessionView, states ...string) *productprotocol.TurnView {
	for i := len(view.Turns) - 1; i >= 0; i-- {
		for _, state := range states {
			if view.Turns[i].State == string(state) {
				return &view.Turns[i]
			}
		}
	}
	return nil
}

func chatSettled(t *productprotocol.TurnView, reviewSettles bool) *chatEnd {
	if t == nil {
		return nil
	}
	switch t.State {
	case chatStateTerminal:
		return &chatEnd{outcome: t.Outcome, reason: t.TerminalReason}
	case chatStateUnknown:
		return &chatEnd{outcome: "unknown"}
	case chatStateAbandoned:
		return &chatEnd{outcome: "abandoned"}
	case chatStateAwaitingReview:
		if reviewSettles && t.Resolution == "" && !t.CancelRequested {
			return &chatEnd{review: true}
		}
	}
	return nil
}

// cancelTurn asks the service to cancel. Cancelling is idempotent and its intent is unambiguous, so
// unlike a prompt it is re-read and re-sent with a new key when the revision went stale underneath.
func (s *chatSession) cancelTurn(ctx context.Context, turnID string) (bool, error) {
	var err error
	for range 3 {
		var view *productprotocol.SessionView
		if view, err = s.refresh(ctx); err != nil {
			break
		}
		if _, err = s.call(ctx, productprotocol.Request{Op: productprotocol.OpCancelTurn, CancelTurn: &productprotocol.CancelTurn{
			SessionID: s.id, TurnID: turnID, ExpectedRevision: view.Revision, RequestKey: chatNewKey()}}); err == nil {
			fmt.Fprintf(s.stdout, "cancel requested for %s; waiting for it to settle\n", chatTurnLabel(turnID))
			return true, nil
		}
		if !chatIsCode(err, string(productsession.CodeStaleRevision)) {
			break
		}
	}
	return false, s.fail(ctx, err)
}

func (s *chatSession) cancelPending(ctx context.Context) error {
	view, err := s.refresh(ctx)
	if err != nil {
		return s.fail(ctx, err)
	}
	t := chatPending(view, chatStateAdmitted, chatStateLinked, chatStateAwaitingReview)
	if t == nil {
		fmt.Fprintln(s.stdout, "no turn is running or awaiting review")
		return nil
	}
	if !t.CancelRequested {
		if sent, err := s.cancelTurn(ctx, t.ID); !sent {
			return err
		}
	}
	return s.settle(ctx, t.ID, false)
}

func (s *chatSession) resolvePending(ctx context.Context, decision string) error {
	view, err := s.refresh(ctx)
	if err != nil {
		return s.fail(ctx, err)
	}
	t := chatPending(view, chatStateAwaitingReview)
	if t == nil || t.Resolution != "" || t.CancelRequested {
		fmt.Fprintln(s.stdout, "no turn is awaiting a review decision")
		return nil
	}
	return s.review(ctx, t.ID, decision)
}

// review shows the turn's candidate and resolves it against exactly that candidate's digest. The
// preset decision publish still asks when its diff was not shown before in this process.
func (s *chatSession) review(ctx context.Context, turnID, preset string) error {
	resp, err := s.call(ctx, productprotocol.Request{Op: productprotocol.OpGetCandidate, GetCandidate: &productprotocol.TurnRef{SessionID: s.id, TurnID: turnID}})
	if err != nil {
		return s.fail(ctx, err)
	}
	cand := resp.Candidate
	if cand == nil || cand.CandidateDigest == "" {
		return s.fail(ctx, &chatServiceError{Code: "PROTOCOL_ERROR", Detail: "response has no candidate"})
	}
	seen := s.shown[cand.CandidateDigest]
	if !seen {
		printCandidate(s.stdout, cand)
		s.shown[cand.CandidateDigest] = true
	}
	decision := preset
	if preset == "" || (preset == chatDecisionPublish && !seen) {
		if decision, err = s.ask(ctx); err != nil {
			return err
		}
		if decision == "/cancel" {
			if sent, err := s.cancelTurn(ctx, turnID); !sent {
				return err
			}
			return s.settle(ctx, turnID, false)
		}
		if decision == "" {
			fmt.Fprintln(s.stdout, "kept: the turn stays awaiting review (/publish or /discard later)")
			return nil
		}
	}
	view, err := s.refresh(ctx)
	if err != nil {
		return s.fail(ctx, err)
	}
	if _, err := s.call(ctx, productprotocol.Request{Op: productprotocol.OpResolveTurn, ResolveTurn: &productprotocol.ResolveTurn{
		SessionID: s.id, TurnID: turnID, ExpectedRevision: view.Revision, RequestKey: chatNewKey(),
		CandidateDigest: cand.CandidateDigest, Decision: decision}}); err != nil {
		return s.fail(ctx, err)
	}
	fmt.Fprintf(s.stdout, "%s requested for %s\n", decision, chatTurnLabel(turnID))
	return s.settle(ctx, turnID, false)
}

// ask returns publish, discard, /cancel, or "" for keep. A detach leaves the review pending.
func (s *chatSession) ask(ctx context.Context) (string, error) {
	for {
		fmt.Fprint(s.stdout, "[p]ublish / [d]iscard / [k]eep? ")
		line, err := s.readLine()
		if err != nil {
			fmt.Fprintln(s.stdout)
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "p", "publish", "/publish":
			return chatDecisionPublish, nil
		case "d", "discard", "/discard":
			return chatDecisionDiscard, nil
		case "/cancel":
			return "/cancel", nil
		case "/quit":
			return "", errChatDetach
		case "/status":
			if err := s.showStatus(ctx); err != nil {
				return "", err
			}
		case "k", "keep", "":
			return "", nil
		}
	}
}
