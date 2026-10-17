// Package researchroute is the host side of the research provider route: the
// thesis's final runs use OpenRouter deepseek/deepseek-v4.1-flash under a hard
// US$5 total cap. It loads the frozen research profile and the owner's ledger
// configuration from fixed root-owned files, builds the provider route (endpoint,
// model and request members) from the profile, opens the durable USD ledger, and
// accounts every provider request against it: reserve a worst case before the
// request leaves the process, settle the reported cost afterwards, and mark the
// unit uncertain when the outcome is not known.
//
// The engineering route (OpenCode Go) never imports or opens any of this. The
// package fails closed: an unfrozen profile, a missing or insecure key, a missing
// ledger (a ledger is created only by the explicit Genesis), a cap or stop-rule
// refusal, and any unknown cost all refuse or keep the reservation counted.
//
// Decisions left to the owner are inputs, not constants: the cap, the stop-rule
// threshold and the reserve basis come from the ledger configuration, and the
// provider pin and price ceilings come from the profile. The defaults chosen where
// the existing APIs left a gap are documented at their use:
//
//   - every provider request is its own ledger unit, settled to its actual cost;
//   - the reserve basis selects the worst case of one request or of one unit;
//   - every price is the larger of the declared price and the profile's price
//     ceiling, and the ceilings (provider.max_price, required by the frozen
//     profile) are sent on every request, so the provider refuses to route above
//     them in any pricing period, peak or base; reservation and token-only
//     settlement price at that ceiling;
//   - actual cost is the provider-reported usage.cost when present; a cost above
//     the usage tokens priced at the ceiling is an anomaly and is recorded with
//     SettleOverage; without usage.cost the tokens priced at the ceiling are the
//     cost;
//   - max_input_tokens is enforced before the reservation: the request is refused,
//     with nothing reserved or sent, when its serialized body (an upper bound of
//     its input tokens) is longer than max_input_tokens. The reservation then
//     assumes max_input_tokens in full, and a larger actual cost is recorded with
//     SettleOverage, after which the ledger refuses every later request.
//
// Passing tests verify this code only. They establish neither budget feasibility
// nor real prices, provider routing or provider behaviour.
package researchroute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/researchprofile"
	"tbound/supervisor/internal/usdledger"
)

// Fixed host paths. The key is read with the same rules as the engineering key
// (a regular file owned by the service uid, mode 0400 or 0600, no symlink, the
// environment ignored). The profile and the ledger configuration must be owned
// by root. The ledger lives under the service StateDirectory
// /var/lib/tbound/sessions, which the unchanged unit template already makes
// writable.
const (
	KeyPath          = "/etc/tbound/provider/openrouter.key"
	ProfilePath      = "/etc/tbound/provider/research-profile.json"
	LedgerConfigPath = "/etc/tbound/provider/research-ledger.json"
	LedgerPath       = "/var/lib/tbound/sessions/usd-ledger"

	maxProfileFileBytes = 64 << 10
)

// Errors returned by this package. Match them with errors.Is.
var (
	ErrConfigFile      = errors.New("research configuration file is missing or insecure")
	ErrKey             = errors.New("research provider key file is unavailable")
	ErrLedgerExists    = errors.New("research ledger already exists; genesis is a one-time act")
	ErrStopRule        = errors.New("research ledger stop rule: the remaining cap is below the per-unit worst case")
	ErrNoCompletionCap = errors.New("research route needs a host-selected completion-token cap")
	ErrSpendOverage    = errors.New("provider cost exceeded its reservation; the ledger recorded the overage and refuses further requests")
	ErrUsageMissing    = errors.New("provider response carries no usable usage; the cost is unknown")
	ErrWrongRequest    = errors.New("request is not built from this research profile")
	ErrInputOverBound  = errors.New("research request body exceeds the frozen max_input_tokens bound")
)

// Paths names the files of the research route. DefaultPaths gives the fixed
// production paths. OwnerUID is the uid that must own the profile and the ledger
// configuration; it is root (0) in production and is a field only so that tests
// can use files they own.
type Paths struct {
	Profile      string
	LedgerConfig string
	Ledger       string
	Key          string
	OwnerUID     uint32
}

// DefaultPaths returns the fixed production paths.
func DefaultPaths() Paths {
	return Paths{Profile: ProfilePath, LedgerConfig: LedgerConfigPath, Ledger: LedgerPath, Key: KeyPath}
}

// Bundle is an opened research route. It holds the ledger lock until Close and
// implements providerbridge.SpendAccountant.
type Bundle struct {
	profile      researchprofile.Profile
	digest       string
	route        openrouter.Route
	ledgerConfig LedgerConfig
	ledgerDigest string
	keyPath      string
	ledger       *usdledger.Ledger

	mu       sync.Mutex
	reserved map[string]int64
}

var _ providerbridge.SpendAccountant = (*Bundle)(nil)

type inputs struct {
	profile      researchprofile.Profile
	digest       string
	route        openrouter.Route
	ledgerConfig LedgerConfig
	ledgerDigest string
}

// Open loads the frozen profile and the ledger configuration, checks the key, and
// opens the existing ledger. It never creates the ledger: a missing ledger is
// refused with usdledger.ErrMissingLedger unless Genesis was run.
func Open(paths Paths) (*Bundle, error) { return open(paths, "") }

// open is Open with an optional endpoint override that only tests use, to point
// the route at a loopback fake server.
func open(paths Paths, endpoint string) (*Bundle, error) {
	in, err := load(paths, endpoint)
	if err != nil {
		return nil, err
	}
	if _, err := loadKey(paths.Key); err != nil {
		return nil, err
	}
	ledger, err := usdledger.Open(paths.Ledger, ledgerOptions(in.ledgerConfig, false))
	if err != nil {
		return nil, fmt.Errorf("open research USD ledger: %w", err)
	}
	return &Bundle{profile: in.profile, digest: in.digest, route: in.route, ledgerConfig: in.ledgerConfig, ledgerDigest: in.ledgerDigest,
		keyPath: paths.Key, ledger: ledger, reserved: make(map[string]int64)}, nil
}

// Genesis creates the research ledger. It is the one explicit act that permits a
// new ledger file, so a deleted ledger is refused at Open rather than reset to an
// empty budget. It refuses an unfrozen profile, an invalid ledger configuration
// and an existing ledger, and it creates nothing in those cases. It returns the
// genesis snapshot, whose HeadSHA256 the operator should store outside the ledger.
func Genesis(paths Paths) (usdledger.Snapshot, error) {
	in, err := load(paths, "")
	if err != nil {
		return usdledger.Snapshot{}, err
	}
	if _, err := os.Lstat(paths.Ledger); err == nil {
		return usdledger.Snapshot{}, ErrLedgerExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return usdledger.Snapshot{}, err
	}
	ledger, err := usdledger.Open(paths.Ledger, ledgerOptions(in.ledgerConfig, true))
	if err != nil {
		return usdledger.Snapshot{}, fmt.Errorf("create research USD ledger: %w", err)
	}
	snapshot, snapshotErr := ledger.Snapshot()
	return snapshot, errors.Join(snapshotErr, ledger.Close())
}

// ledgerOptions deliberately leaves usdledger.Config.ExpectedHeadSHA256 unset.
// That check compares the ledger's CURRENT head hash, which changes with every
// appended record, so an anchor written once at genesis would match only a ledger
// that has not yet recorded a reservation, and the service would refuse to reopen
// its own ledger after the first request. Detecting rollback to an older copy,
// truncation at a record boundary or a tail edit needs an anchor that is updated
// as the ledger grows, and nothing in the current API or file layout provides one
// that the service cannot also rewrite. What this route does catch: a deleted
// ledger (Open refuses a missing file without Genesis), a foreign cap or ledger
// ID, a broken hash chain and a torn record.
//
// TODO(owner-decision): who owns the head-hash anchor (an operator-held copy of
// Snapshot.HeadSHA256 compared before each run, an append-only or remote log, or
// a root-owned file updated by a separate privileged process) is open. Until it
// is decided, rollback of the ledger to an earlier valid state is NOT detected.
func ledgerOptions(config LedgerConfig, genesis bool) usdledger.Config {
	return usdledger.Config{LedgerID: config.LedgerID, CapMicroUSD: config.CapMicroUSD,
		MinPerUnitWorstCaseMicroUSD: config.MinPerUnitWorstCaseMicroUSD, AllowGenesis: genesis}
}

func load(paths Paths, endpoint string) (inputs, error) {
	profileBytes, err := readOwnedFile(paths.Profile, maxProfileFileBytes, paths.OwnerUID)
	if err != nil {
		return inputs{}, err
	}
	profile, err := researchprofile.Load(bytes.NewReader(profileBytes))
	if err != nil {
		return inputs{}, fmt.Errorf("research profile: %w", err)
	}
	if reason := profile.FrozenReason(); reason != nil {
		return inputs{}, fmt.Errorf("%w: %w", researchprofile.ErrNotFrozen, reason)
	}
	digest, err := profile.Digest()
	if err != nil {
		return inputs{}, fmt.Errorf("research profile: %w", err)
	}
	members, err := profile.RequestMembers()
	if err != nil {
		return inputs{}, err
	}
	if endpoint == "" {
		endpoint = profile.Endpoint
	}
	route, err := openrouter.NewResearchRoute(openrouter.ResearchSpec{Model: profile.Model, Endpoint: endpoint,
		ProfileDigest: digest, Members: members, Timeout: durationMS(*profile.TimeoutMS),
		ResponseModels: profile.ResponseModels()})
	if err != nil {
		return inputs{}, fmt.Errorf("research route: %w", err)
	}
	configBytes, err := readOwnedFile(paths.LedgerConfig, maxLedgerConfigBytes, paths.OwnerUID)
	if err != nil {
		return inputs{}, err
	}
	ledgerConfig, err := LoadLedgerConfig(bytes.NewReader(configBytes))
	if err != nil {
		return inputs{}, err
	}
	b := &Bundle{profile: profile, ledgerConfig: ledgerConfig}
	if _, err := b.reserveAmount(*profile.MaxOutputTokens); err != nil {
		return inputs{}, err
	}
	ledgerDigest, err := ledgerConfig.Digest()
	if err != nil {
		return inputs{}, err
	}
	return inputs{profile: profile, digest: digest, route: route, ledgerConfig: ledgerConfig, ledgerDigest: ledgerDigest}, nil
}

// loadKey reads the research key with the engineering key's loader rules. Only
// the _FILE lookup is exposed, so neither the process environment nor a
// caller-chosen path is ever consulted.
func loadKey(path string) (openrouter.Credentials, error) {
	credentials, err := openrouter.LoadCredentialsFrom(func(name string) (string, bool) {
		if name == openrouter.APIKeyFileEnv {
			return path, true
		}
		return "", false
	})
	if err != nil {
		return openrouter.Credentials{}, errors.Join(ErrKey,
			fmt.Errorf("%s must be a regular file owned by the service uid with mode 0400 or 0600, at most 4096 bytes, holding one printable API key: %w", path, err))
	}
	return credentials, nil
}

// Route is the provider route built from the frozen profile.
func (b *Bundle) Route() openrouter.Route { return b.route }

// ProfileDigest is the lowercase hex SHA-256 of the canonical frozen profile.
func (b *Bundle) ProfileDigest() string { return b.digest }

// MaxRequestsPerUnit is the profile's frozen request bound per unit.
func (b *Bundle) MaxRequestsPerUnit() uint64 { return *b.profile.MaxRequestsPerUnit }

// Credentials re-reads the key file, so a rotated key needs no restart and the
// key is not retained beyond the transport that uses it.
func (b *Bundle) Credentials() (openrouter.Credentials, error) { return loadKey(b.keyPath) }

// Snapshot reports the ledger totals.
func (b *Bundle) Snapshot() (usdledger.Snapshot, error) { return b.ledger.Snapshot() }

// Close releases the ledger lock.
func (b *Bundle) Close() error { return b.ledger.Close() }

// ProviderProfileDigest binds the research profile and the ledger configuration
// (ledger ID, cap, stop rule, reserve basis) into the provider-profile digest that
// the workflow runtime pin already commits to. The result has the same
// "sha256:<hex>" shape as the host digest it extends, and it changes with any of
// the three inputs, so recovery and replay refuse a changed ledger configuration
// exactly as they refuse a changed profile.
func (b *Bundle) ProviderProfileDigest(hostDigest string) string {
	sum := sha256.Sum256([]byte("tbound-research-provider-profile/v2\x00" + hostDigest + "\x00" + b.digest + "\x00" + b.ledgerDigest))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// reserveAmount is the worst-case micro-USD to reserve for one request with the
// given completion cap, on the configured reserve basis. max_input_tokens is
// assumed in full.
func (b *Bundle) reserveAmount(completionLimit uint64) (int64, error) {
	var amount uint64
	var err error
	switch b.ledgerConfig.ReserveBasis {
	case ReserveBasisRequest:
		amount, err = b.profile.WorstCaseMicroUSD(*b.profile.MaxInputTokens, completionLimit)
	case ReserveBasisUnit:
		amount, err = b.profile.WorstCaseUnitMicroUSD(*b.profile.MaxInputTokens, completionLimit)
	default:
		err = fmt.Errorf("%w: unknown reserve basis", ErrLedgerConfig)
	}
	if err != nil {
		return 0, fmt.Errorf("worst-case research reservation: %w", err)
	}
	if amount == 0 || amount > math.MaxInt64 {
		return 0, errors.New("worst-case research reservation is not a positive int64 micro-USD amount")
	}
	return int64(amount), nil
}

// ReserveSpend implements providerbridge.SpendAccountant. It refuses, before the
// request is sent, when the ledger recorded an overage (usdledger.ErrOverCap),
// when the stop rule holds (ErrStopRule), when the worst case does not fit the
// remaining cap (usdledger.ErrCapExceeded), when the host selected no completion
// cap, or when the request was not built from this profile.
func (b *Bundle) ReserveSpend(_ context.Context, reservation providerbridge.SpendReservation) (string, error) {
	if reservation.CompletionLimit == 0 {
		return "", ErrNoCompletionCap
	}
	snapshot, err := b.ledger.Snapshot()
	if err != nil {
		return "", err
	}
	if snapshot.Overspent {
		return "", usdledger.ErrOverCap
	}
	if snapshot.StopRule {
		return "", fmt.Errorf("%w (remaining %d micro-USD, threshold %d)", ErrStopRule,
			snapshot.RemainingMicroUSD, snapshot.MinPerUnitWorstCaseMicroUSD)
	}
	request := reservation.Request
	body := request.Body()
	if request.URL() != b.route.Endpoint || !bytes.HasSuffix(body, []byte(","+b.route.Members()[1:])) {
		return "", ErrWrongRequest
	}
	// Input-token bound. The reservation prices max_input_tokens in full, so it is
	// an upper bound on the charge only if the request cannot carry more input than
	// that. Nothing else limits the input: system and developer prompts, history and
	// tool definitions are all in the body. So the exact serialized body that will
	// be sent (every message, every tool definition and all framing, including the
	// frozen profile members) is bounded here, before anything is reserved.
	//
	// len(body) is the bound. The provider's tokenizer is a byte-level BPE, in which
	// every token covers at least one byte of its input and no token is produced
	// from nothing, so the number of tokens the content yields is at most its UTF-8
	// byte length; the content is no longer than the body that carries it. A token
	// per byte is the worst case and is far above real prompts (about 3 to 4 bytes
	// per token), so this refuses some bodies that would have fit, never one that
	// would not. No tighter bound is claimed. Chat-template special tokens are not
	// in the body and are a small constant per message, so the reservation keeps the
	// full frozen max_input_tokens and does not shrink to the bound.
	if bound := uint64(len(body)); bound > *b.profile.MaxInputTokens {
		return "", fmt.Errorf("%w: the request body is %d bytes (an upper bound of its input tokens), max_input_tokens is %d",
			ErrInputOverBound, bound, *b.profile.MaxInputTokens)
	}
	amount, err := b.reserveAmount(reservation.CompletionLimit)
	if err != nil {
		return "", err
	}
	unit := unitID(reservation)
	if err := b.ledger.Reserve(unit, amount); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.reserved[unit] = amount
	b.mu.Unlock()
	return unit, nil
}

// unitID names one request. The ledger refuses a repeated unit, so a request
// cannot be reserved twice.
func unitID(r providerbridge.SpendReservation) string {
	sum := sha256.New()
	fmt.Fprintf(sum, "tbound-research-call/v1\x00%s\x00%s\x00%d", r.WorkflowID, r.ConversationID, r.Turn)
	return "research-call/" + hex.EncodeToString(sum.Sum(nil))
}

// ReleaseSpend implements providerbridge.SpendAccountant for a request that never
// left the process: the unit settles at zero.
func (b *Bundle) ReleaseSpend(_ context.Context, unitID string) error {
	if err := b.ledger.Settle(unitID, 0); err != nil {
		return err
	}
	b.forget(unitID)
	return nil
}

// UncertainSpend implements providerbridge.SpendAccountant. The full reservation
// stays counted against the cap until an operator settles it.
func (b *Bundle) UncertainSpend(_ context.Context, unitID string) error {
	return b.ledger.MarkUncertain(unitID)
}

// SettleSpend implements providerbridge.SpendAccountant. The actual cost is the
// provider-reported usage.cost (US$, rounded up to micro-USD) when present, else
// the usage token counts priced at the profile's price ceilings (see actualCost).
// A usage.cost above the tokens priced at the ceilings is an anomaly: the
// ceilings were sent with the request, so the provider cannot have charged more,
// and the cost is recorded with SettleOverage. A cost within the reservation and
// the ceiling price is settled; a larger one is recorded with SettleOverage and
// reported as ErrSpendOverage, after which the ledger refuses every later
// reservation. When no cost can be established the unit is marked uncertain and
// ErrUsageMissing is returned. In every case the unit is left in its final state.
func (b *Bundle) SettleSpend(_ context.Context, unitID string, usage json.RawMessage) error {
	b.mu.Lock()
	reserved, known := b.reserved[unitID]
	b.mu.Unlock()
	if !known {
		return b.uncertainWith(unitID, usdledger.ErrUnknownUnit)
	}
	actual, anomaly, err := b.actualCost(usage)
	if err != nil {
		return b.uncertainWith(unitID, err)
	}
	if anomaly && actual <= reserved {
		// The ledger records a stop only through SettleOverage, and refuses it for an
		// amount within the reservation. The anomaly is therefore booked at one
		// micro-USD above the reservation: never below the reported cost, and the
		// ledger then refuses every later reservation.
		actual = reserved + 1
	}
	if actual <= reserved {
		if err := b.ledger.Settle(unitID, actual); err != nil {
			return b.uncertainWith(unitID, err)
		}
		b.forget(unitID)
		return nil
	}
	if err := b.ledger.SettleOverage(unitID, actual); err != nil {
		return b.uncertainWith(unitID, err)
	}
	b.forget(unitID)
	return fmt.Errorf("%w: reserved %d, actual %d micro-USD", ErrSpendOverage, reserved, actual)
}

func (b *Bundle) forget(unitID string) {
	b.mu.Lock()
	delete(b.reserved, unitID)
	b.mu.Unlock()
}

// uncertainWith marks the unit uncertain after cause and returns both errors.
func (b *Bundle) uncertainWith(unitID string, cause error) error {
	return errors.Join(cause, b.ledger.MarkUncertain(unitID))
}

func durationMS(ms uint64) time.Duration { return time.Duration(ms) * time.Millisecond }

// LoadProfileDigest validates the frozen profile and the ledger configuration at
// paths and returns the profile digest that the route and the workflow runtime
// pin will carry.
func LoadProfileDigest(paths Paths) (string, error) {
	in, err := load(paths, "")
	if err != nil {
		return "", err
	}
	return in.digest, nil
}
