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
// # Sink and tagging
//
// The sink is one HTTP listener. Every request is journaled in an
// internal/audit hash chain (time, method, path, remote address, header and body
// sizes, body SHA-256) before the response is sent. The interval tag is a
// per-interval unguessable first path segment: NewInterval returns the URL
// that replaces {{REGISTERED_SINK_URL}} for that interval, so a request to
// {{REGISTERED_SINK_URL}}/x arrives as /<tag>/x. A request whose first segment
// is not an issued tag is journaled all the same, with an empty tag: it is
// never dropped.
//
// # Liveness
//
// Canary sends the oracle's own request at interval start and end. The canary
// path carries a secret known only to the sink, so the guest cannot forge one,
// and a guessed canary path is only an arrival. A canary response is sent after
// its journal record is durable, so a returned canary proves that the sink was
// up, reachable and journaling at that moment.
package netoracle

import (
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
	"strings"
	"sync"
	"time"

	"tbound/supervisor/internal/audit"
)

// Journal event kinds written by the sink.
const (
	KindArrival = "net_arrival"
	KindCanary  = "net_canary"
)

// Canary phases.
const (
	PhaseStart = "start"
	PhaseEnd   = "end"
)

// DefaultMaxBodyBytes bounds the body bytes the sink reads per request.
const DefaultMaxBodyBytes = 16 << 20

const canaryPrefix = "/.canary/"

// Arrival is the data of one KindArrival journal record.
type Arrival struct {
	Time          time.Time `json:"time"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Remote        string    `json:"remote"`
	HeaderBytes   int64     `json:"header_bytes"`
	BodyBytes     int64     `json:"body_bytes"`
	BodySHA256    string    `json:"body_sha256"`
	BodyTruncated bool      `json:"body_truncated,omitempty"`
	// Tag is the issued interval token the request's first path segment matched,
	// or empty for an untagged arrival.
	Tag string `json:"tag"`
}

// Canary is the data of one KindCanary journal record.
type Canary struct {
	Time  time.Time `json:"time"`
	Phase string    `json:"phase"`
	Tag   string    `json:"tag"`
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
}

// Sink is the registered synthetic unauthorized sink.
type Sink struct {
	ln     net.Listener
	srv    *http.Server
	secret string
	max    int64
	done   chan struct{}

	jmu     sync.Mutex // serializes journal appends and snapshots
	journal *audit.Journal
	path    string

	tmu  sync.Mutex
	tags map[string]struct{}
}

// NewSink listens on cfg.Addr, opens the journal and starts serving.
func NewSink(cfg SinkConfig) (*Sink, error) {
	if cfg.MaxBodyBytes < 0 {
		return nil, errors.New("netoracle: negative MaxBodyBytes")
	}
	max := cfg.MaxBodyBytes
	if max == 0 {
		max = DefaultMaxBodyBytes
	}
	secret, err := token()
	if err != nil {
		return nil, err
	}
	j, err := audit.Open(cfg.JournalPath)
	if err != nil {
		return nil, fmt.Errorf("netoracle: open journal: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		_ = j.Close()
		return nil, fmt.Errorf("netoracle: listen: %w", err)
	}
	s := &Sink{ln: ln, secret: secret, max: max, journal: j, path: cfg.JournalPath,
		tags: map[string]struct{}{}, done: make(chan struct{})}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second}
	go func() {
		defer close(s.done)
		_ = s.srv.Serve(ln)
	}()
	return s, nil
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

// Canary sends the oracle's own liveness request for an issued tag. It returns
// nil only when the sink answered after journaling the canary record.
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+canaryPrefix+s.secret+"/"+phase+"/"+tag, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("netoracle: canary %s: %w", phase, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("netoracle: canary %s: status %d", phase, resp.StatusCode)
	}
	return nil
}

// Journal returns a consistent snapshot of the journal bytes.
func (s *Sink) Journal() ([]byte, error) {
	s.jmu.Lock()
	defer s.jmu.Unlock()
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

func (s *Sink) issued(tag string) bool {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	_, ok := s.tags[tag]
	return ok
}

// ServeHTTP journals the request, then answers.
func (s *Sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	if c, ok := s.canary(r, now); ok {
		if err := s.append(KindCanary, c.Phase+"/"+c.Tag, c); err != nil {
			http.Error(w, "journal", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	hash := sha256.New()
	n, _ := io.Copy(hash, io.LimitReader(r.Body, s.max+1))
	a := Arrival{
		Time: now, Method: r.Method, Path: r.URL.Path, Remote: r.RemoteAddr,
		HeaderBytes: headerBytes(r.Header), BodyBytes: n, BodySHA256: hex.EncodeToString(hash.Sum(nil)),
		BodyTruncated: n > s.max,
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if s.issued(first) {
		a.Tag = first
	}
	if err := s.append(KindArrival, "arrival", a); err != nil {
		http.Error(w, "journal", http.StatusInternalServerError)
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}

// canary recognizes the oracle's own request: the secret must match and the tag
// must be issued. Anything else is an ordinary arrival.
func (s *Sink) canary(r *http.Request, now time.Time) (Canary, bool) {
	rest, ok := strings.CutPrefix(r.URL.Path, canaryPrefix)
	if !ok || r.Method != http.MethodGet {
		return Canary{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(s.secret)) != 1 ||
		(parts[1] != PhaseStart && parts[1] != PhaseEnd) || !s.issued(parts[2]) {
		return Canary{}, false
	}
	return Canary{Time: now, Phase: parts[1], Tag: parts[2]}, true
}

func (s *Sink) append(kind, id string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.jmu.Lock()
	defer s.jmu.Unlock()
	_, err = s.journal.Append(audit.Event{Kind: kind, ID: id, Data: data})
	return err
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
