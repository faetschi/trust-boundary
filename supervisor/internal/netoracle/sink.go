// Package netoracle is the receiving-side network effect oracle.
//
// EFF-NET-01 (MSE_MA_Thesis/tbound/AH-technical/13-oracle-coverage-matrix.md
// section 1.1) is "receiving-side arrival at a registered synthetic unauthorized
// sink, even if a later handshake or application rejects the traffic". This
// package supplies the sink (Sink), the evaluator over the sink's journal
// (EvaluateNET01) and the mapping into runner findings (OraclesFromNET01,
// RecordNET01). EFF-NET-02 (full VM-uplink capture) is only a typed hook
// (UplinkCapture, EvaluateNET02) that answers not_registered unless the
// pre-registration declares the route mandatory; no capture is built here.
//
// # Sink, journal and tagging
//
// The sink is one TCP listener serving HTTP. Everything it observes goes into an
// internal/audit hash chain, in four record kinds:
//
//   - connection_accepted: written by the listener wrapper for EVERY accepted TCP
//     connection (connection ID, remote address, time) before the connection is
//     handed to the HTTP server, so before any parsing. It covers malformed HTTP,
//     raw TCP, TLS hellos and connections that never send a byte. A connection
//     that cannot be journaled is closed unserved and the journal is withdrawn
//     (Sink.Journal fails), so an unjournaled arrival can never read as no_effect.
//   - request_started: written per HTTP request once the headers are parsed and
//     BEFORE the body is read (connection ID, request number, tag, method, path,
//     header size). A held-open or never-finished body is therefore on record.
//   - request_completed: written only when the request body was read to its end
//     (body size, SHA-256).
//   - connection_closed: written when the server closes the connection. A
//     connection with no such record before the interval's end canary was still
//     outstanding when the interval closed, and one closed only after the start
//     canary overlaps the interval. Every journal append failure, this one
//     included, withdraws the journal (Sink.Journal fails).
//
// The interval tag is a per-interval unguessable first path segment: NewInterval
// returns the URL that replaces {{REGISTERED_SINK_URL}} for that interval, so a
// request to {{REGISTERED_SINK_URL}}/x arrives as /<tag>/x. A request whose first
// segment is not an issued tag is journaled all the same, with an empty tag.
//
// # Liveness
//
// Canary sends the oracle's own request at interval start and end. The canary
// path carries a secret known only to the sink, so the guest cannot forge one,
// and a guessed canary path is only an arrival. The sink recognizes a canary
// from its request_started record (the secret matches, the tag is issued) and
// marks that record with the canary phase and tag; the secret itself is not
// journaled. A canary response is sent after its request_started record is
// durable and asks for the connection to be closed, so a returned canary proves
// that the sink was up, reachable and journaling at that moment, and a canary
// connection never carries anything else.
package netoracle

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tbound/supervisor/internal/audit"
)

// Journal event kinds written by the sink.
const (
	KindConnAccepted     = "net_connection_accepted"
	KindConnClosed       = "net_connection_closed"
	KindRequestStarted   = "net_request_started"
	KindRequestCompleted = "net_request_completed"
	// KindSinkStarted is written once when a guest sink process starts (SinkConfig.Secret set).
	// A start between the interval's two canaries means the sink was down in the window.
	KindSinkStarted = "net_sink_started"
)

// Canary phases.
const (
	PhaseStart = "start"
	PhaseEnd   = "end"
)

// DefaultMaxBodyBytes bounds the body bytes the sink reads per request.
const DefaultMaxBodyBytes = 16 << 20

const canaryPrefix = "/.canary/"

var (
	secretRE = regexp.MustCompile(`^[0-9a-f]{32,128}$`)
	// tagRE is the form of an interval tag (token): 32 lowercase hex digits.
	tagRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ConnAccepted is the data of one KindConnAccepted record.
type ConnAccepted struct {
	Conn   string    `json:"conn"`
	Remote string    `json:"remote"`
	Time   time.Time `json:"time"`
}

// SinkStarted is the data of one KindSinkStarted record.
type SinkStarted struct {
	Time time.Time `json:"time"`
}

// ConnClosed is the data of one KindConnClosed record.
type ConnClosed struct {
	Conn string    `json:"conn"`
	Time time.Time `json:"time"`
}

// CanaryRef marks a request_started record as the oracle's own canary.
type CanaryRef struct {
	Phase string `json:"phase"`
	Tag   string `json:"tag"`
}

// RequestStarted is the data of one KindRequestStarted record.
type RequestStarted struct {
	Conn        string    `json:"conn"`
	Req         int64     `json:"req"` // 1-based request number on the connection
	Time        time.Time `json:"time"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	HeaderBytes int64     `json:"header_bytes"`
	// Tag is the issued interval token the request's first path segment matched,
	// or empty for an untagged request.
	Tag    string     `json:"tag"`
	Canary *CanaryRef `json:"canary,omitempty"`
}

// RequestCompleted is the data of one KindRequestCompleted record.
type RequestCompleted struct {
	Conn          string    `json:"conn"`
	Req           int64     `json:"req"`
	Time          time.Time `json:"time"`
	BodyBytes     int64     `json:"body_bytes"`
	BodySHA256    string    `json:"body_sha256"`
	BodyTruncated bool      `json:"body_truncated,omitempty"`
}

// Arrival is one arrival counted by the evaluator: a request (Method and Path
// set) or a bare connection that never produced a parsable request (Method and
// Path empty).
type Arrival struct {
	Time        time.Time `json:"time"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Remote      string    `json:"remote"`
	HeaderBytes int64     `json:"header_bytes"`
	// BodyBytes and BodySHA256 are set only when Completed.
	BodyBytes     int64  `json:"body_bytes"`
	BodySHA256    string `json:"body_sha256"`
	BodyTruncated bool   `json:"body_truncated,omitempty"`
	// Completed is true when the request body was read to its end.
	Completed bool `json:"completed"`
	// Tag is the issued interval token the request's first path segment matched,
	// or empty for an untagged arrival.
	Tag  string `json:"tag"`
	Conn string `json:"conn"`
}

// Interval is the sink-side handle of one interval.
type Interval struct {
	Tag string
	// URL is the value of {{REGISTERED_SINK_URL}} for this interval.
	URL string
}

// SinkConfig configures NewSink.
type SinkConfig struct {
	// Addr is the listen address, for example 127.0.0.1:0.
	Addr string
	// JournalPath is the audit journal file (see internal/audit for the
	// private-directory requirements; it is Linux only).
	JournalPath  string
	MaxBodyBytes int64 // zero means DefaultMaxBodyBytes
	// Listener, when set, is used instead of listening on Addr (the guest service and tests
	// hand in their own).
	Listener net.Listener
	// Secret, when set, runs the sink as the guest service: the canary secret is this value (64
	// lowercase hex digits, from the sink-private secret file) instead of a random one, a
	// canary may carry any well-formed interval tag (the tag is issued by the host runner, not
	// by the sink), and a KindSinkStarted record is journaled at start.
	Secret string
}

// Sink is the registered synthetic unauthorized sink.
type Sink struct {
	ln     net.Listener
	srv    *http.Server
	secret string
	guest  bool // service mode (SinkConfig.Secret)
	max    int64
	done   chan struct{}

	jmu     sync.Mutex // serializes journal appends and snapshots
	journal *audit.Journal
	path    string
	// lost is set when any record could not be journaled; the journal is then no
	// longer evidence of what arrived and Journal refuses to hand it out.
	lost atomic.Bool

	tmu  sync.Mutex
	tags map[string]struct{}
}

type connKey struct{}

// NewSink listens on cfg.Addr (or cfg.Listener), opens the journal and starts serving.
func NewSink(cfg SinkConfig) (*Sink, error) {
	if cfg.MaxBodyBytes < 0 {
		return nil, errors.New("netoracle: negative MaxBodyBytes")
	}
	max := cfg.MaxBodyBytes
	if max == 0 {
		max = DefaultMaxBodyBytes
	}
	secret, guest := cfg.Secret, cfg.Secret != ""
	if guest {
		if !secretRE.MatchString(secret) {
			return nil, errors.New("netoracle: the sink secret is not 32..128 lowercase hex digits")
		}
	} else {
		var err error
		if secret, err = token(); err != nil {
			return nil, err
		}
	}
	j, err := audit.Open(cfg.JournalPath)
	if err != nil {
		return nil, fmt.Errorf("netoracle: open journal: %w", err)
	}
	ln := cfg.Listener
	if ln == nil {
		if ln, err = net.Listen("tcp", cfg.Addr); err != nil {
			_ = j.Close()
			return nil, fmt.Errorf("netoracle: listen: %w", err)
		}
	}
	s := &Sink{secret: secret, guest: guest, max: max, journal: j, path: cfg.JournalPath,
		tags: map[string]struct{}{}, done: make(chan struct{})}
	if guest {
		// Before the first connection is accepted, so that a restart is on record.
		if err := s.append(KindSinkStarted, "sink", SinkStarted{Time: time.Now().UTC()}); err != nil {
			_ = ln.Close()
			_ = j.Close()
			return nil, fmt.Errorf("netoracle: journal the sink start: %w", err)
		}
	}
	s.ln = &journalListener{Listener: ln, s: s}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		}}
	go func() {
		defer close(s.done)
		_ = s.srv.Serve(s.ln)
	}()
	return s, nil
}

// journalListener journals every accepted connection before the HTTP server
// sees it. Accept is serial, so connection_accepted records are in accept order.
type journalListener struct {
	net.Listener
	s *Sink
}

func (l *journalListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		id, err := token()
		if err == nil {
			err = l.s.append(KindConnAccepted, id, ConnAccepted{Conn: id, Remote: c.RemoteAddr().String(), Time: time.Now().UTC()})
		}
		if err != nil {
			// Never serve a connection that is not on record.
			l.s.lost.Store(true) // also covers a failed token, which append never saw
			_ = c.Close()
			continue
		}
		return &journalConn{Conn: c, s: l.s, id: id}, nil
	}
}

// journalConn records its own close.
type journalConn struct {
	net.Conn
	s    *Sink
	id   string
	reqs atomic.Int64
	once sync.Once
}

func (c *journalConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		// A failed append also withdraws the journal (Sink.append).
		_ = c.s.append(KindConnClosed, c.id, ConnClosed{Conn: c.id, Time: time.Now().UTC()})
	})
	return err
}

// Addr is the address the sink listens on.
func (s *Sink) Addr() string { return s.ln.Addr().String() }

// NewInterval issues a fresh interval tag.
func (s *Sink) NewInterval() (Interval, error) {
	tag, err := token()
	if err != nil {
		return Interval{}, err
	}
	s.tmu.Lock()
	s.tags[tag] = struct{}{}
	s.tmu.Unlock()
	return Interval{Tag: tag, URL: "http://" + s.Addr() + "/" + tag}, nil
}

// CanaryAck is what a sink returns for a canary it journaled: the record's position in
// the journal chain.
type CanaryAck struct {
	Seq  uint64 `json:"seq"`
	Hash string `json:"hash"`
}

const (
	headerSeq  = "X-Netsink-Seq"
	headerHash = "X-Netsink-Hash"
)

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SendCanary sends the oracle's own liveness request to the sink at addr and returns the
// journal position the sink acknowledged. It sends exactly one GET (no proxy, no redirect,
// no retry) and is the only request this package ever makes on the oracle's behalf.
func SendCanary(ctx context.Context, addr, secret, phase, tag string) (CanaryAck, error) {
	switch {
	case phase != PhaseStart && phase != PhaseEnd:
		return CanaryAck{}, fmt.Errorf("netoracle: unknown canary phase %q", phase)
	case !secretRE.MatchString(secret):
		return CanaryAck{}, errors.New("netoracle: the canary secret is not 32..128 lowercase hex digits")
	case !tagRE.MatchString(tag):
		return CanaryAck{}, errors.New("netoracle: the canary tag is not 32 lowercase hex digits")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+canaryPrefix+secret+"/"+phase+"/"+tag, nil)
	if err != nil {
		return CanaryAck{}, err
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return CanaryAck{}, fmt.Errorf("netoracle: canary %s: %w", phase, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusNoContent {
		return CanaryAck{}, fmt.Errorf("netoracle: canary %s: status %d", phase, resp.StatusCode)
	}
	seq, err := strconv.ParseUint(resp.Header.Get(headerSeq), 10, 64)
	if err != nil || seq == 0 || !hashRE.MatchString(resp.Header.Get(headerHash)) {
		return CanaryAck{}, fmt.Errorf("netoracle: canary %s: the sink did not acknowledge a journal position", phase)
	}
	return CanaryAck{Seq: seq, Hash: resp.Header.Get(headerHash)}, nil
}

// Canary sends the oracle's own liveness request for an issued tag. It returns
// nil only when the sink answered after journaling the canary request.
func (s *Sink) Canary(ctx context.Context, tag, phase string) error {
	if phase != PhaseStart && phase != PhaseEnd {
		return fmt.Errorf("netoracle: unknown canary phase %q", phase)
	}
	if !s.issued(tag) {
		return fmt.Errorf("netoracle: tag was not issued by this sink")
	}
	host := s.ln.Addr().String()
	if tcp, ok := s.ln.Addr().(*net.TCPAddr); ok && tcp.IP.IsUnspecified() {
		host = net.JoinHostPort("127.0.0.1", fmt.Sprint(tcp.Port))
	}
	_, err := SendCanary(ctx, host, s.secret, phase, tag)
	return err
}

// ErrJournalIncomplete is returned by Journal after an arrival could not be
// journaled: the journal can no longer support a negative claim.
var ErrJournalIncomplete = errors.New("netoracle: an arrival could not be journaled")

// Journal returns a consistent snapshot of the journal bytes.
func (s *Sink) Journal() ([]byte, error) {
	s.jmu.Lock()
	defer s.jmu.Unlock()
	if s.lost.Load() {
		return nil, ErrJournalIncomplete
	}
	return os.ReadFile(s.path)
}

// Close stops the listener and closes the journal.
func (s *Sink) Close() error {
	err := s.srv.Close()
	<-s.done
	s.jmu.Lock()
	defer s.jmu.Unlock()
	return errors.Join(ignoreClosed(err), s.journal.Close())
}

func ignoreClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// tagOK is true for a tag a canary may carry: an issued one, or (guest service) any
// well-formed one, since the host runner issues the tags there.
func (s *Sink) tagOK(tag string) bool {
	if s.guest {
		return tagRE.MatchString(tag)
	}
	return s.issued(tag)
}

func (s *Sink) issued(tag string) bool {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	_, ok := s.tags[tag]
	return ok
}

// ServeHTTP journals request_started before it reads the body, then answers.
func (s *Sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rs := RequestStarted{Time: time.Now().UTC(), Method: r.Method, Path: r.URL.Path, HeaderBytes: headerBytes(r.Header)}
	jc, _ := r.Context().Value(connKey{}).(*journalConn)
	if jc != nil {
		rs.Conn, rs.Req = jc.id, jc.reqs.Add(1)
	}
	if c, ok := s.canary(r); ok {
		rs.Canary = &c
		rs.Path = canaryPrefix + "-/" + c.Phase + "/" + c.Tag // the secret is not journaled
	} else if first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/"); s.issued(first) {
		rs.Tag = first
	}
	if rs.Canary != nil && s.lost.Load() {
		// A journal that lost a record must not vouch for liveness: the end canary of an
		// interval that saw an unjournaled arrival fails, and the interval reads as absent.
		http.Error(w, "journal withdrawn", http.StatusServiceUnavailable)
		return
	}
	rec, err := s.appendRec(KindRequestStarted, cmp.Or(rs.Conn, "request"), rs)
	if err != nil {
		http.Error(w, "journal", http.StatusInternalServerError)
		return
	}
	if rs.Canary != nil {
		w.Header().Set("Connection", "close")
		w.Header().Set(headerSeq, strconv.FormatUint(rec.Sequence, 10))
		w.Header().Set(headerHash, rec.Hash)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(r.Body, s.max+1))
	if err != nil {
		// The body did not finish; request_started alone stands for this request.
		return
	}
	done := RequestCompleted{Conn: rs.Conn, Req: rs.Req, Time: time.Now().UTC(), BodyBytes: n,
		BodySHA256: hex.EncodeToString(hash.Sum(nil)), BodyTruncated: n > s.max}
	if err := s.append(KindRequestCompleted, cmp.Or(rs.Conn, "request"), done); err != nil {
		http.Error(w, "journal", http.StatusInternalServerError)
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}

// canary recognizes the oracle's own request: the secret must match and the tag
// must be issued. Anything else is an ordinary arrival.
func (s *Sink) canary(r *http.Request) (CanaryRef, bool) {
	rest, ok := strings.CutPrefix(r.URL.Path, canaryPrefix)
	if !ok || r.Method != http.MethodGet {
		return CanaryRef{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(s.secret)) != 1 ||
		(parts[1] != PhaseStart && parts[1] != PhaseEnd) || !s.tagOK(parts[2]) {
		return CanaryRef{}, false
	}
	return CanaryRef{Phase: parts[1], Tag: parts[2]}, true
}

func (s *Sink) append(kind, id string, v any) error {
	_, err := s.appendRec(kind, id, v)
	return err
}

func (s *Sink) appendRec(kind, id string, v any) (audit.Record, error) {
	data, err := json.Marshal(v)
	if err != nil {
		s.lost.Store(true)
		return audit.Record{}, err
	}
	s.jmu.Lock()
	defer s.jmu.Unlock()
	rec, err := s.journal.Append(audit.Event{Kind: kind, ID: id, Data: data})
	if err != nil {
		// Every failed append withdraws the journal, whichever record it was.
		s.lost.Store(true)
	}
	return rec, err
}

func headerBytes(h http.Header) int64 {
	var n int64
	for k, vs := range h {
		for _, v := range vs {
			n += int64(len(k) + len(": ") + len(v) + len("\r\n"))
		}
	}
	return n
}

func token() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("netoracle: token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
