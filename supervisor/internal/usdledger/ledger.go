// Package usdledger is a durable, fail-closed monetary budget ledger for one
// spend domain. Callers reserve a worst-case cost before each provider send,
// settle the actual cost when the outcome is known, and mark unknown outcomes
// uncertain. Open and uncertain reservations stay counted against the cap until
// an explicit Settle releases the remainder. All amounts are integer micro-USD
// (1 USD = 1,000,000). The package has no provider, network, or pricing code;
// callers pass in worst cases. Use one Ledger per spend domain, for example one
// for the research OpenRouter budget and a separate one for engineering usage.
//
// Storage is an append-only JSON-lines file. Each record stores the SHA-256 of
// the previous record's exact bytes, and each append is fsynced before the call
// returns. Open takes an exclusive nonblocking flock, which is Linux-only; other
// platforms fail closed with ErrUnsupported. Open replays and verifies the whole
// file. It refuses corruption, torn final records, foreign caps, and ledger-ID
// mismatches, and it never rewrites an existing file. Reservations left open by
// a crash are reported as uncertain, so a crash never releases budget. A missing
// file is refused unless Config.AllowGenesis is set. An actual cost above its
// reservation is recorded only by SettleOverage, and after one, every Reserve is
// refused with ErrOverCap for the life of the ledger.
//
// The chain is unkeyed. It detects torn, reordered, deleted, and edited records
// that are not recomputed, but anyone who can rewrite the file can recompute it.
// The newest record has no later link, so tail edits, truncation at a record
// boundary, rollback to an older copy, and deletion are detected only against
// Config.ExpectedHeadSHA256, a head hash stored outside the file.
//
// Passing tests verify this code only. They do not establish budget feasibility,
// G1, or H1.
package usdledger

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	maxIDBytes     = 128
	maxRecordBytes = 1024
	zeroHash       = "0000000000000000000000000000000000000000000000000000000000000000"
	genesisKind    = "genesis"
	reserveKind    = "reserve"
	settleKind     = "settle"
	uncertainKind  = "uncertain"
	overageKind    = "overage"
)

// Errors returned by this package. Match them with errors.Is. Errors that
// describe corrupt file content also match ErrCorrupt.
var (
	ErrUnsupported       = errors.New("usdledger needs Linux advisory file locking")
	ErrInvalidConfig     = errors.New("invalid usdledger configuration")
	ErrInvalidID         = errors.New("invalid ledger or unit identifier")
	ErrInvalidAmount     = errors.New("amount is not a valid micro-USD value for this operation")
	ErrInvalidLedgerFile = errors.New("usdledger path must be a regular file, not a symlink")
	ErrLocked            = errors.New("usdledger is held by another writer")
	ErrCorrupt           = errors.New("usdledger file is corrupt or truncated")
	ErrTornRecord        = errors.New("usdledger final record is incomplete")
	ErrChainBroken       = errors.New("usdledger hash chain does not verify")
	ErrLedgerIDMismatch  = errors.New("usdledger file belongs to a different ledger ID")
	ErrCapMismatch       = errors.New("usdledger file was created with a different cap")
	ErrHeadMismatch      = errors.New("usdledger head does not match the expected anchor")
	ErrCapExceeded       = errors.New("reservation would exceed the usdledger cap")
	ErrUnitExists        = errors.New("unit already has a reservation in this ledger")
	ErrUnknownUnit       = errors.New("unit has no reservation in this ledger")
	ErrOverSettle        = errors.New("actual cost exceeds the reservation")
	ErrNotOverage        = errors.New("actual cost does not exceed the reservation; use Settle")
	ErrOverCap           = errors.New("usdledger recorded an overage; no further reservations")
	ErrSettleConflict    = errors.New("unit was already settled with a different amount")
	ErrAlreadySettled    = errors.New("unit is already settled")
	ErrMissingLedger     = errors.New("usdledger file is absent and AllowGenesis is false")
	ErrLedgerReplaced    = errors.New("usdledger path was replaced while it was being opened")
	ErrClosed            = errors.New("usdledger is closed")
	ErrPoisoned          = errors.New("usdledger is fail-closed after a write error; reopen to verify")
	errAlreadyUncertain  = errors.New("unit is already uncertain")
)

// syncRecord fsyncs the ledger after a record is written. It is a variable only
// so that tests can inject a failure in the landed-but-not-durable window.
var syncRecord = func(f *os.File) error { return f.Sync() }

// Config binds a Ledger file to one spend domain. Open refuses a file whose
// genesis record disagrees with LedgerID or CapMicroUSD.
type Config struct {
	// LedgerID names the spend domain, for example the research OpenRouter
	// budget. It is written to the genesis record and must match on every Open.
	LedgerID string

	// CapMicroUSD is the total cap in integer micro-USD. It is fixed when the
	// file is created.
	CapMicroUSD int64

	// MinPerUnitWorstCaseMicroUSD is the stop-rule threshold reported by
	// Snapshot. It is not stored in the file and must be in (0, CapMicroUSD].
	MinPerUnitWorstCaseMicroUSD int64

	// ExpectedHeadSHA256, when set, must equal the file's head at Open. A missing
	// file is refused rather than created, so deleting the ledger cannot reset
	// spent budget. Store Snapshot.HeadSHA256 outside the ledger.
	ExpectedHeadSHA256 string

	// AllowGenesis lets Open create the file when it is absent. It defaults to
	// false, so a missing or deleted ledger is refused rather than reset to an
	// empty budget. Set it only when a fresh ledger is intended.
	AllowGenesis bool
}

// Snapshot is a point-in-time view of one ledger.
type Snapshot struct {
	LedgerID                    string
	CapMicroUSD                 int64
	MinPerUnitWorstCaseMicroUSD int64

	// Money fields are integer micro-USD. OpenMicroUSD is reserved by Reserve
	// calls in this process that have not been settled or marked uncertain.
	// UncertainMicroUSD is the full reservation of each uncertain unit, including
	// open reservations recovered from an earlier process.
	OpenMicroUSD      int64
	UncertainMicroUSD int64
	SettledMicroUSD   int64
	CommittedMicroUSD int64
	RemainingMicroUSD int64

	// StopRule is true when RemainingMicroUSD is below
	// MinPerUnitWorstCaseMicroUSD. Reserve does not enforce it; callers check it
	// before starting each unit.
	StopRule bool

	// Overspent is true after a recorded overage. Every later Reserve is refused
	// with ErrOverCap for the life of the ledger.
	Overspent bool

	OpenUnits      int
	UncertainUnits int
	SettledUnits   int

	// Records counts the genesis record and every appended record. HeadSHA256
	// hashes the newest record; store it outside the ledger to anchor the file.
	Records    uint64
	HeadSHA256 string
}

// Ledger is a single-writer handle that holds the file lock until Close. Its
// methods are safe for concurrent use.
type Ledger struct {
	mu       sync.Mutex
	cfg      Config
	file     *os.File
	book     book
	poisoned error
	closed   bool
}

// record is one JSON line. Fields that do not apply to a kind are omitted.
// Seq and Prev chain each line to the one before it.
type record struct {
	Seq    uint64 `json:"seq"`
	Prev   string `json:"prev"`
	Kind   string `json:"kind"`
	Ledger string `json:"ledger,omitempty"`
	Cap    int64  `json:"cap,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Micro  int64  `json:"micro,omitempty"`
}

type unitState uint8

const (
	unitOpen unitState = iota + 1
	unitUncertain
	unitSettled
)

// unit is one reservation in the projection. settled is meaningful only when
// state is unitSettled.
type unit struct {
	state    unitState
	reserved int64
	settled  int64
}

// book is the projection of the chain. Replay and live operations both go
// through verify or check and then apply, so each invariant has one definition.
type book struct {
	ledgerID       string
	capMicro       int64
	units          map[string]*unit
	openMicro      int64
	uncertainMicro int64
	settledMicro   int64
	seq            uint64
	head           string
	// overspent is set by an overage record and is never cleared. Reserve is
	// refused from then on, so a worst-case bound that was violated cannot keep
	// admitting new spend.
	overspent bool
}

func newBook(cfg Config) book {
	return book{
		ledgerID: cfg.LedgerID,
		capMicro: cfg.CapMicroUSD,
		units:    make(map[string]*unit),
		head:     zeroHash,
	}
}

func (b *book) committed() int64 {
	return b.openMicro + b.uncertainMicro + b.settledMicro
}

// check reports whether rec is legal now. It does not mutate the projection.
func (b *book) check(rec record) error {
	if err := validID(rec.Unit); err != nil {
		return err
	}
	switch rec.Kind {
	case reserveKind:
		if b.overspent {
			return ErrOverCap
		}
		if _, ok := b.units[rec.Unit]; ok {
			return ErrUnitExists
		}
		if rec.Micro <= 0 {
			return ErrInvalidAmount
		}
		if rec.Micro > b.capMicro-b.committed() {
			return ErrCapExceeded
		}
	case settleKind:
		u, ok := b.units[rec.Unit]
		if !ok {
			return ErrUnknownUnit
		}
		if u.state == unitSettled {
			return ErrAlreadySettled
		}
		if rec.Micro < 0 {
			return ErrInvalidAmount
		}
		if rec.Micro > u.reserved {
			return ErrOverSettle
		}
	case overageKind:
		u, ok := b.units[rec.Unit]
		if !ok {
			return ErrUnknownUnit
		}
		if u.state == unitSettled {
			return ErrAlreadySettled
		}
		if rec.Micro <= u.reserved {
			return ErrNotOverage
		}
		// Committed spend after this record is committed - reserved + actual. It
		// must stay within int64 so that every later sum is exact.
		if rec.Micro > math.MaxInt64-(b.committed()-u.reserved) {
			return fmt.Errorf("%w: committed total would overflow", ErrInvalidAmount)
		}
	case uncertainKind:
		u, ok := b.units[rec.Unit]
		if !ok {
			return ErrUnknownUnit
		}
		if u.state == unitSettled {
			return ErrAlreadySettled
		}
		if u.state == unitUncertain {
			return errAlreadyUncertain
		}
	default:
		return fmt.Errorf("unknown record kind %q", rec.Kind)
	}
	return nil
}

// apply updates the projection. Callers must have passed check first.
func (b *book) apply(rec record) {
	switch rec.Kind {
	case reserveKind:
		b.units[rec.Unit] = &unit{state: unitOpen, reserved: rec.Micro}
		b.openMicro += rec.Micro
	case settleKind, overageKind:
		u := b.units[rec.Unit]
		if u.state == unitOpen {
			b.openMicro -= u.reserved
		} else {
			b.uncertainMicro -= u.reserved
		}
		u.state = unitSettled
		u.settled = rec.Micro
		b.settledMicro += rec.Micro
		if rec.Kind == overageKind {
			b.overspent = true
		}
	case uncertainKind:
		u := b.units[rec.Unit]
		b.openMicro -= u.reserved
		b.uncertainMicro += u.reserved
		u.state = unitUncertain
	}
}

// verify checks a non-genesis record during replay. Its fields must match its
// kind, and check must accept it against the records before it.
func (b *book) verify(rec record) error {
	if rec.Kind == genesisKind || rec.Ledger != "" || rec.Cap != 0 {
		return errors.New("record fields do not match its kind")
	}
	if rec.Kind == uncertainKind && rec.Micro != 0 {
		return errors.New("uncertain record carries an amount")
	}
	return b.check(rec)
}

// recoverOpen reports reservations left open by an earlier process as
// uncertain. It writes nothing; the file keeps the open state, and every later
// Open reports it the same way.
func (b *book) recoverOpen() {
	for _, u := range b.units {
		if u.state == unitOpen {
			u.state = unitUncertain
			b.openMicro -= u.reserved
			b.uncertainMicro += u.reserved
		}
	}
}

// Open verifies an existing ledger against cfg. A missing file is created only
// when cfg.AllowGenesis is true and cfg.ExpectedHeadSHA256 is empty. Otherwise
// Open refuses it with ErrMissingLedger, or with ErrHeadMismatch when an anchor
// is set, because a deleted ledger must not reopen as an empty budget. Open
// never rewrites an existing file.
func Open(path string, cfg Config) (*Ledger, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	anchored := cfg.ExpectedHeadSHA256 != ""
	file, created, err := openLedgerFile(path, cfg.AllowGenesis && !anchored)
	if errors.Is(err, ErrMissingLedger) && anchored {
		return nil, fmt.Errorf("%w: ledger file is absent", ErrHeadMismatch)
	}
	if err != nil {
		return nil, err
	}
	l := &Ledger{cfg: cfg, file: file, book: newBook(cfg)}
	if created {
		if err := l.commit(record{Kind: genesisKind, Ledger: cfg.LedgerID, Cap: cfg.CapMicroUSD}); err != nil {
			return nil, abortCreate(path, file, err)
		}
		if err := syncDir(filepath.Dir(path)); err != nil {
			return nil, abortCreate(path, file, fmt.Errorf("sync ledger directory: %w", err))
		}
		return l, nil
	}
	b, err := replay(file, cfg)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if cfg.ExpectedHeadSHA256 != "" && b.head != cfg.ExpectedHeadSHA256 {
		_ = file.Close()
		return nil, ErrHeadMismatch
	}
	b.recoverOpen()
	l.book = b
	return l, nil
}

// Reserve records a worst-case reservation for unitID before the provider send.
// The reservation is durable when Reserve returns nil. A non-nil error means the
// caller must not send; the reservation may still have become durable. After a
// recorded overage, Reserve returns ErrOverCap whatever the remaining headroom.
func (l *Ledger) Reserve(unitID string, worstCaseMicroUSD int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if err := validID(unitID); err != nil {
		return err
	}
	if worstCaseMicroUSD <= 0 {
		return fmt.Errorf("%w: worst case must be positive", ErrInvalidAmount)
	}
	rec := record{Kind: reserveKind, Unit: unitID, Micro: worstCaseMicroUSD}
	if err := l.book.check(rec); err != nil {
		return err
	}
	return l.commit(rec)
}

// Settle records the actual cost of unitID and releases the rest of its
// reservation. Settling an uncertain unit is how its budget is released. Settling
// again with the same amount is a no-op; with a different amount it fails. An
// actual cost above the reservation is refused with ErrOverSettle and writes
// nothing; record that overage with SettleOverage.
func (l *Ledger) Settle(unitID string, actualMicroUSD int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if err := validID(unitID); err != nil {
		return err
	}
	if actualMicroUSD < 0 {
		return fmt.Errorf("%w: actual cost cannot be negative", ErrInvalidAmount)
	}
	u, ok := l.book.units[unitID]
	if !ok {
		return ErrUnknownUnit
	}
	if u.state == unitSettled {
		if u.settled == actualMicroUSD {
			return nil
		}
		return ErrSettleConflict
	}
	rec := record{Kind: settleKind, Unit: unitID, Micro: actualMicroUSD}
	if err := l.book.check(rec); err != nil {
		return err
	}
	return l.commit(rec)
}

// SettleOverage records an actual cost above the reservation of unitID. Settle
// refuses such a cost, so this is the only way to record real overspend. The
// full actual amount counts toward committed spend, even above the cap, and the
// ledger refuses every later Reserve with ErrOverCap for its whole life. A cost
// at or below the reservation is refused with ErrNotOverage; use Settle for it.
// Repeating the same amount is a no-op, and a different amount fails with
// ErrSettleConflict.
func (l *Ledger) SettleOverage(unitID string, actualMicroUSD int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if err := validID(unitID); err != nil {
		return err
	}
	if actualMicroUSD < 0 {
		return fmt.Errorf("%w: actual cost cannot be negative", ErrInvalidAmount)
	}
	u, ok := l.book.units[unitID]
	if !ok {
		return ErrUnknownUnit
	}
	if actualMicroUSD <= u.reserved {
		return ErrNotOverage
	}
	if u.state == unitSettled {
		if u.settled == actualMicroUSD {
			return nil
		}
		return ErrSettleConflict
	}
	rec := record{Kind: overageKind, Unit: unitID, Micro: actualMicroUSD}
	if err := l.book.check(rec); err != nil {
		return err
	}
	return l.commit(rec)
}

// MarkUncertain records that the outcome of unitID is unknown. The full
// reservation stays counted until Settle. Marking an uncertain unit again is a
// no-op.
func (l *Ledger) MarkUncertain(unitID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if err := validID(unitID); err != nil {
		return err
	}
	u, ok := l.book.units[unitID]
	if !ok {
		return ErrUnknownUnit
	}
	switch u.state {
	case unitSettled:
		return ErrAlreadySettled
	case unitUncertain:
		return nil
	}
	rec := record{Kind: uncertainKind, Unit: unitID}
	if err := l.book.check(rec); err != nil {
		return err
	}
	return l.commit(rec)
}

// Snapshot reports totals, the stop-rule flag, and the head hash.
func (l *Ledger) Snapshot() (Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{
		LedgerID:                    l.cfg.LedgerID,
		CapMicroUSD:                 l.cfg.CapMicroUSD,
		MinPerUnitWorstCaseMicroUSD: l.cfg.MinPerUnitWorstCaseMicroUSD,
		OpenMicroUSD:                l.book.openMicro,
		UncertainMicroUSD:           l.book.uncertainMicro,
		SettledMicroUSD:             l.book.settledMicro,
		Records:                     l.book.seq,
		HeadSHA256:                  l.book.head,
		Overspent:                   l.book.overspent,
	}
	s.CommittedMicroUSD = l.book.committed()
	s.RemainingMicroUSD = s.CapMicroUSD - s.CommittedMicroUSD
	s.StopRule = s.RemainingMicroUSD < s.MinPerUnitWorstCaseMicroUSD
	for _, u := range l.book.units {
		switch u.state {
		case unitOpen:
			s.OpenUnits++
		case unitUncertain:
			s.UncertainUnits++
		case unitSettled:
			s.SettledUnits++
		}
	}
	return s, nil
}

// Close releases the file lock. It is safe to call more than once.
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.file.Close()
}

func (l *Ledger) usable() error {
	if l.closed {
		return ErrClosed
	}
	return l.poisoned
}

// commit appends rec, fsyncs it, and only then applies it to the projection.
// A write or sync error poisons the handle. The file may then hold a partial or
// complete record, and a reopen verifies which.
func (l *Ledger) commit(rec record) error {
	rec.Seq = l.book.seq
	rec.Prev = l.book.head
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line := append(body, '\n')
	if _, err := l.file.Write(line); err != nil {
		return l.poison(fmt.Errorf("append record: %w", err))
	}
	if err := syncRecord(l.file); err != nil {
		return l.poison(fmt.Errorf("sync record: %w", err))
	}
	l.book.apply(rec)
	l.book.seq++
	l.book.head = hashHex(body)
	return nil
}

func (l *Ledger) poison(cause error) error {
	l.poisoned = fmt.Errorf("%w: %w", ErrPoisoned, cause)
	return l.poisoned
}

func (c Config) validate() error {
	if err := validID(c.LedgerID); err != nil {
		return err
	}
	if c.CapMicroUSD <= 0 {
		return fmt.Errorf("%w: cap must be positive", ErrInvalidConfig)
	}
	if c.MinPerUnitWorstCaseMicroUSD <= 0 || c.MinPerUnitWorstCaseMicroUSD > c.CapMicroUSD {
		return fmt.Errorf("%w: per-unit minimum must be in (0, cap]", ErrInvalidConfig)
	}
	if c.ExpectedHeadSHA256 != "" && !isHash(c.ExpectedHeadSHA256) {
		return fmt.Errorf("%w: expected head must be 64 lowercase hex digits", ErrInvalidConfig)
	}
	return nil
}

// validID accepts ASCII letters, digits, and ._-:/+ so that identifiers are
// unambiguous in the JSON lines and in receipts.
func validID(id string) error {
	if id == "" || len(id) > maxIDBytes {
		return ErrInvalidID
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("._-:/+", c) >= 0) {
			return ErrInvalidID
		}
	}
	return nil
}

func isHash(s string) bool {
	if len(s) != sha256.Size*2 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func hashHex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// abortCreate closes and removes a ledger file that this Open created, after its
// genesis write or directory sync failed. Nothing was reserved, so removing the
// genesis-only file loses no budget. The file is removed only while the path still
// names it. Every cleanup failure is joined to cause, so a leftover file is visible.
func abortCreate(path string, file *os.File, cause error) error {
	errs := []error{cause}
	info, statErr := file.Stat()
	if closeErr := file.Close(); closeErr != nil {
		errs = append(errs, fmt.Errorf("close partial ledger: %w", closeErr))
	}
	switch {
	case statErr != nil:
		errs = append(errs, fmt.Errorf("stat partial ledger: %w", statErr))
	default:
		pathInfo, err := os.Lstat(path)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("stat partial ledger path: %w", err))
		case !os.SameFile(info, pathInfo):
			errs = append(errs, errors.New("partial ledger path was replaced; not removed"))
		default:
			if err := os.Remove(path); err != nil {
				errs = append(errs, fmt.Errorf("remove partial ledger: %w", err))
			}
		}
	}
	return errors.Join(errs...)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	if closeErr := d.Close(); syncErr == nil {
		return closeErr
	}
	return syncErr
}

// replay verifies every record in file and returns the resulting projection. It
// does not write. A torn final record is refused, not repaired.
func replay(file *os.File, cfg Config) (book, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return book{}, fmt.Errorf("rewind ledger: %w", err)
	}
	reader := bufio.NewReaderSize(file, maxRecordBytes)
	b := newBook(cfg)
	for n := uint64(0); ; n++ {
		raw, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) {
			if len(raw) > 0 {
				return book{}, fmt.Errorf("%w: %w: record %d", ErrCorrupt, ErrTornRecord, n)
			}
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			return book{}, fmt.Errorf("%w: record %d exceeds %d bytes", ErrCorrupt, n, maxRecordBytes)
		}
		if err != nil {
			return book{}, fmt.Errorf("read ledger: %w", err)
		}
		body := raw[:len(raw)-1]
		rec, err := parseRecord(body)
		if err != nil {
			return book{}, fmt.Errorf("%w: record %d: %w", ErrCorrupt, n, err)
		}
		if rec.Seq != n || rec.Prev != b.head {
			return book{}, fmt.Errorf("%w: %w: record %d", ErrCorrupt, ErrChainBroken, n)
		}
		if n == 0 {
			if err := checkGenesis(rec, cfg); err != nil {
				return book{}, err
			}
		} else {
			if err := b.verify(rec); err != nil {
				return book{}, fmt.Errorf("%w: record %d: %w", ErrCorrupt, n, err)
			}
			b.apply(rec)
		}
		b.seq = n + 1
		b.head = hashHex(body)
	}
	if b.seq == 0 {
		return book{}, fmt.Errorf("%w: file has no genesis record", ErrCorrupt)
	}
	return b, nil
}

func checkGenesis(rec record, cfg Config) error {
	if rec.Kind != genesisKind || rec.Unit != "" || rec.Micro != 0 {
		return fmt.Errorf("%w: first record is not genesis", ErrCorrupt)
	}
	if rec.Ledger != cfg.LedgerID {
		return ErrLedgerIDMismatch
	}
	if rec.Cap != cfg.CapMicroUSD {
		return ErrCapMismatch
	}
	return nil
}

// parseRecord decodes one line. It accepts only the exact bytes the writer
// produces, so every accepted record is canonical.
func parseRecord(body []byte) (record, error) {
	var rec record
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return record{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return record{}, errors.New("trailing data after record")
	}
	canonical, err := json.Marshal(rec)
	if err != nil {
		return record{}, err
	}
	if !bytes.Equal(canonical, body) {
		return record{}, errors.New("record is not in canonical form")
	}
	return rec, nil
}

// Unit lifecycle states reported by Unit.
const (
	UnitStateOpen      = "open"
	UnitStateUncertain = "uncertain"
	UnitStateSettled   = "settled"
)

// UnitInfo is the lifecycle of one reservation. SettledMicroUSD is meaningful only for a
// settled unit.
type UnitInfo struct {
	ID               string
	State            string
	ReservedMicroUSD int64
	SettledMicroUSD  int64
}

// Unit reports the reservation of unitID, or found == false when this ledger holds none. It
// reads the projection only. A reservation that was open when an earlier process ended is
// uncertain here, so "open" means a request that this handle reserved and has not yet settled
// or marked uncertain.
func (l *Ledger) Unit(unitID string) (info UnitInfo, found bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return UnitInfo{}, false, err
	}
	if err := validID(unitID); err != nil {
		return UnitInfo{}, false, err
	}
	u, ok := l.book.units[unitID]
	if !ok {
		return UnitInfo{}, false, nil
	}
	info = UnitInfo{ID: unitID, ReservedMicroUSD: u.reserved}
	switch u.state {
	case unitOpen:
		info.State = UnitStateOpen
	case unitUncertain:
		info.State = UnitStateUncertain
	case unitSettled:
		info.State, info.SettledMicroUSD = UnitStateSettled, u.settled
	}
	return info, true, nil
}
