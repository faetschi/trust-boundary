package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"tbound/supervisor/internal/evalcorpus"
)

// ErrReceiptPolicy means a receipt carries a targeted configuration delta that the
// registered plan does not allow for the step: any delta on a C receipt, or a P delta
// that is not the manifest's p_delta.
var ErrReceiptPolicy = errors.New("runner: receipt violates the registered delta policy")

// DeltaNone is the delta of a receipt without one; an empty delta means the same.
const DeltaNone = "none"

// ReceiptPins are the receipt identity values the host expects. They are the host's
// registered pins for the run, not read from any executor. E01 is verified only when the
// receipt of the step carries exactly these three values.
type ReceiptPins struct {
	ProfileHash              string `json:"profile_hash"`
	ContainmentProfileDigest string `json:"containment_profile_digest"`
	FrozenSourceDigest       string `json:"frozen_source_digest"`
}

// Validate refuses pins with an empty member: an empty pin would verify an empty field.
func (p ReceiptPins) Validate() error {
	if p.ProfileHash == "" || p.ContainmentProfileDigest == "" || p.FrozenSourceDigest == "" {
		return errors.New("receipt pins need profile_hash, containment_profile_digest and frozen_source_digest")
	}
	return nil
}

func (p ReceiptPins) match(r *ReceiptFacts) bool {
	return r != nil && p.Validate() == nil && r.ProfileHash == p.ProfileHash &&
		r.ContainmentProfileDigest == p.ContainmentProfileDigest && r.FrozenSourceDigest == p.FrozenSourceDigest
}

// Attest decorates an Executor with what the host, and not the guest-backed executor,
// establishes about a trial: GovernedC leaves Channel, E01.Verified, Route, EVQ and
// MatchedDelivery empty.
//
//   - Channel is the step's registered predicate (per-predicate witnesses are independent,
//     so the predicate names the channel). It is set for adversarial steps only.
//   - E01.Verified is true only when the receipt facts equal Pins. A missing receipt or
//     any other value is unverified, never an error.
//   - EVQ is never manufactured here and an executor result is never overwritten. Receipt
//     identity establishes E01.Verified only. Every configured obligation is reported, and
//     it passes only when the pinned EVQResults (the independently established conformance
//     results of the run) name it as passed and the executor, if it reported the obligation,
//     did not fail it. An obligation without a pinned result, or with a failed one, is
//     recorded as not passed. A failed obligation the executor reported beyond the
//     configured list is kept as reported.
//   - Route is the route report the run was configured with.
//   - MatchedDelivery is true for a static or adaptive replay step whose identity is
//     verified: the replays are scripted and the plan names their stimulus.
//   - A C receipt with a delta, and a P receipt whose delta is not PDelta(case), are
//     refused with ErrReceiptPolicy. The outcome is kept, unverified, and the error makes
//     it partial in the journal.
//
// An empty Outcome (a setup failure or a refusal) is returned untouched, so a failed
// attempt stays a setup failure.
type Attest struct {
	Inner Executor
	Pins  ReceiptPins
	// EVQ lists the obligation IDs reported for every trial. It must not be empty.
	EVQ []string
	// EVQResults are the independently established conformance results (the pinned EVQ
	// results file of the run). They are the only source of a passed obligation.
	EVQResults EVQResults
	Route      RouteCoverage
	// PDelta returns the manifest's p_delta of a case (the empty string for none) and
	// whether the case is known. A nil PDelta refuses every P receipt.
	PDelta func(caseID string) (delta string, ok bool)
}

// Validate checks the configuration of the decorator.
func (a Attest) Validate() error {
	if err := a.Pins.Validate(); err != nil {
		return err
	}
	if len(a.EVQ) == 0 {
		return errors.New("attest needs at least one EVQ obligation")
	}
	for i, id := range a.EVQ {
		if id == "" || slices.Contains(a.EVQ[:i], id) {
			return fmt.Errorf("attest EVQ obligation %q is empty or repeated", id)
		}
	}
	if err := a.EVQResults.Validate(); err != nil {
		return err
	}
	return nil
}

// Prepare implements Executor.
func (a Attest) Prepare(ctx context.Context, s Step) error {
	if a.Inner == nil {
		return ErrNotWired
	}
	if err := a.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrSetupRefused, err)
	}
	if s.Kind == evalcorpus.KindAdversarial && s.Predicate == "" {
		return fmt.Errorf("%w: adversarial step %s has no predicate to name its channel", ErrSetupRefused, s.ID())
	}
	return a.Inner.Prepare(ctx, s)
}

// Execute implements Executor.
func (a Attest) Execute(ctx context.Context, s Step) (Outcome, error) {
	if a.Inner == nil {
		return Outcome{}, ErrNotWired
	}
	out, err := a.Inner.Execute(ctx, s)
	if !hasOutcome(out) {
		return out, err
	}
	verified := a.Pins.match(out.Receipt) && out.E01.Digest != ""
	if perr := a.checkDelta(s, out.Receipt); perr != nil {
		verified = false
		err = errors.Join(err, perr)
	}
	if perr := checkProviderMode(s, out.Receipt); perr != nil {
		verified = false
		err = errors.Join(err, perr)
	}
	out.E01.Verified = verified
	if s.Kind == evalcorpus.KindAdversarial {
		out.Channel = s.Predicate
	}
	out.Route = a.Route
	out.EVQ = a.evq(out.EVQ)
	out.MatchedDelivery = verified && (s.Role == RoleStaticReplay || s.Role == RoleAdaptiveReplay)
	return out, err
}

// evq merges the obligations an executor reported with the pinned conformance results.
// Nothing here derives a pass from the trial itself.
func (a Attest) evq(reported []Obligation) []Obligation {
	pinned := make(map[string]EVQResult, len(a.EVQResults))
	for _, r := range a.EVQResults {
		pinned[r.ID] = r
	}
	out := make([]Obligation, 0, len(a.EVQ)+len(reported))
	for _, id := range a.EVQ {
		r, ok := pinned[id]
		ob := Obligation{ID: id, Passed: ok && r.Passed}
		if ok {
			ob.EvidenceRef = r.EvidenceRef
		}
		for _, got := range reported {
			if got.ID == id && !got.Passed {
				ob.Passed = false
			}
		}
		out = append(out, ob)
	}
	for _, got := range reported {
		if !slices.Contains(a.EVQ, got.ID) {
			out = append(out, got)
		}
	}
	return out
}

// checkDelta applies the delta policy of the configuration. Other configurations are not
// checked here. A step without receipt facts has no delta to compare.
func (a Attest) checkDelta(s Step, r *ReceiptFacts) error {
	if r == nil {
		return nil
	}
	got := normalizeDelta(r.Delta)
	switch s.Configuration {
	case CfgC:
		if got != DeltaNone {
			return fmt.Errorf("%w: C receipt of %s carries delta %q", ErrReceiptPolicy, s.ID(), r.Delta)
		}
	case CfgP:
		if a.PDelta == nil {
			return fmt.Errorf("%w: no manifest p_delta to compare the P receipt of %s with", ErrReceiptPolicy, s.ID())
		}
		want, ok := a.PDelta(s.CaseID)
		if !ok {
			return fmt.Errorf("%w: the manifest has no p_delta for case %q", ErrReceiptPolicy, s.CaseID)
		}
		if got != normalizeDelta(want) {
			return fmt.Errorf("%w: P receipt of %s carries delta %q, the manifest registers %q", ErrReceiptPolicy, s.ID(), r.Delta, want)
		}
	}
	return nil
}

func normalizeDelta(d string) string {
	if d == "" {
		return DeltaNone
	}
	return d
}

// checkProviderMode requires the provider of a replay role to be scripted and the provider of every
// other role to be a model. A receipt without a provider_mode predates the member and is not checked.
func checkProviderMode(s Step, r *ReceiptFacts) error {
	if r == nil || r.ProviderMode == "" {
		return nil
	}
	if s.Role == RoleAdaptiveReplay {
		return nil // adaptive replays are not scripted by this build
	}
	want := "model"
	if s.Role == RoleStaticReplay {
		want = "scripted"
	}
	if r.ProviderMode != want {
		return fmt.Errorf("%w: receipt of %s reports provider_mode %q, the role requires %q", ErrReceiptPolicy, s.ID(), r.ProviderMode, want)
	}
	return nil
}
