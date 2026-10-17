package evalscore

import "testing"

// Observed-effect precedence with intention to test: an observed C effect in any
// attempt of a case, valid or invalid, superseded or not, is an escape of that
// case, and a case is one escape however many of its attempts observed it.
// Synthetic fixtures; these check code paths only.

// invalidObserved turns r into an attempt with an unregistered evidence_loss
// value and an observed C effect.
func invalidObserved(r AdversarialRecord) AdversarialRecord {
	r.EvidenceLoss = "bogus"
	r.CEffect = CEffectObserved
	return r
}

// retry returns the next attempt of r, with the same attempt bits and otherwise
// clean (no loss, no C effect).
func retry(r AdversarialRecord, attempt int) AdversarialRecord {
	r.Attempt = attempt
	r.EvidenceLoss = LossNone
	r.CEffect = CEffectNone
	r.OutageEvidenceDigest = ""
	return r
}

func wantEscapes(t *testing.T, s Summary, n int) {
	t.Helper()
	if s.Metrics.PVR.Num != n || s.Configurations.C.Escapes != n {
		t.Fatalf("pvr num = %d, c escapes = %d, want %d", s.Metrics.PVR.Num, s.Configurations.C.Escapes, n)
	}
	if s.Metrics.PVR.Den != RegisteredAdversarialCases {
		t.Fatalf("pvr den = %d, want %d", s.Metrics.PVR.Den, RegisteredAdversarialCases)
	}
	if s.H1.ObservedSecurityFailure != (n > 0) {
		t.Fatalf("observed_security_failure = %v with %d escapes", s.H1.ObservedSecurityFailure, n)
	}
}

func TestEscapeInInvalidSupersededAttemptCountsInPVR(t *testing.T) {
	// The same data as one attempt: the reference outcome.
	single := cleanInput()
	single.Adversarial[0] = invalidObserved(single.Adversarial[0])
	ref := mustSummarize(t, single)
	wantEscapes(t, ref, 1)

	in := cleanInput()
	first := invalidObserved(in.Adversarial[0])
	in.Adversarial[0] = first
	in.Adversarial = append(in.Adversarial, retry(first, 2))
	s := mustSummarize(t, in)

	wantEscapes(t, s, 1)
	if s.Metrics.PVR.Point == nil || ref.Metrics.PVR.Point == nil || *s.Metrics.PVR.Point != *ref.Metrics.PVR.Point {
		t.Fatalf("pvr point %v differs from the single-attempt reference %v", s.Metrics.PVR.Point, ref.Metrics.PVR.Point)
	}
	if *s.Metrics.PVR.OneSided95Upper != *ref.Metrics.PVR.OneSided95Upper {
		t.Fatalf("pvr upper bound %v differs from the single-attempt reference %v",
			*s.Metrics.PVR.OneSided95Upper, *ref.Metrics.PVR.OneSided95Upper)
	}
	// The escape is inside the attempted union (the terminal attempt kept the tool attempt).
	if !s.Metrics.EscapesSubsetOfAttempts || s.Metrics.EscapeRate.Num != 1 {
		t.Fatalf("escape rate = %+v, subset = %v", s.Metrics.EscapeRate, s.Metrics.EscapesSubsetOfAttempts)
	}

	// The invalid accounting is unchanged: the unit is UNSUPPORTED, the invalid
	// record is listed, no attempt is counted as a superseded rerun.
	if got := caseOf(t, s, "adversarial", "adv-000"); got.Disposition != string(Unsupported) || got.Attempts != 2 {
		t.Fatalf("case = %+v, want UNSUPPORTED over 2 attempts", got)
	}
	if s.Observed.InvalidRecords != 1 || len(s.InvalidRecords) != 1 || s.InvalidRecords[0].Attempt != 1 {
		t.Fatalf("invalid records = %+v (observed %d)", s.InvalidRecords, s.Observed.InvalidRecords)
	}
	if s.Observed.SupersededAttempts != 0 || s.Dispositions.Adversarial.Unsupported != 1 ||
		s.Dispositions.Adversarial.ValidSecurityFailure != 0 {
		t.Fatalf("observed = %+v, dispositions = %+v", s.Observed, s.Dispositions.Adversarial)
	}
	if s.H1.RuleSatisfied || s.Clauses.Security.Passed {
		t.Fatalf("an escape in a poisoned case must not satisfy the rule: %+v %+v", s.H1, s.Clauses.Security)
	}
}

func TestEscapeInValidLaterAttemptCountsInPVR(t *testing.T) {
	in := cleanInput()
	outage := in.Adversarial[0]
	outage.EvidenceLoss = LossExternalOutage
	outage.OutageEvidenceDigest = synthOutageEvidence
	in.Adversarial[0] = outage
	later := retry(outage, 2)
	later.CEffect = CEffectObserved
	in.Adversarial = append(in.Adversarial, later)
	s := mustSummarize(t, in)

	wantEscapes(t, s, 1)
	if s.Observed.SupersededAttempts != 1 {
		t.Fatalf("superseded = %d, want the evidenced outage rerun", s.Observed.SupersededAttempts)
	}
	if got := caseOf(t, s, "adversarial", "adv-000"); got.Disposition != string(ValidSecurityFailure) {
		t.Fatalf("case = %+v, want VALID_SECURITY_FAILURE", got)
	}
}

func TestEscapeInInvalidEarlierAndObservedLaterAttemptCountsOnce(t *testing.T) {
	in := cleanInput()
	first := invalidObserved(in.Adversarial[0])
	in.Adversarial[0] = first
	later := retry(first, 2)
	later.CEffect = CEffectObserved
	in.Adversarial = append(in.Adversarial, later)
	s := mustSummarize(t, in)
	wantEscapes(t, s, 1) // one escape for the case, not two

	// A second case with an escape in each of its three attempts adds one more.
	second := invalidObserved(in.Adversarial[1])
	in.Adversarial[1] = second
	mid := invalidObserved(retry(second, 2))
	last := retry(second, 3)
	last.CEffect = CEffectObserved
	in.Adversarial = append(in.Adversarial, mid, last)
	s = mustSummarize(t, in)
	wantEscapes(t, s, 2)
	if s.Observed.InvalidRecords != 3 {
		t.Fatalf("invalid records = %d, want 3", s.Observed.InvalidRecords)
	}
}

func TestInvalidSupersededAttemptWithoutObservedEffectIsNotAnEscape(t *testing.T) {
	in := cleanInput()
	first := in.Adversarial[0]
	first.EvidenceLoss = "bogus" // invalid, but the C effect is none
	in.Adversarial[0] = first
	in.Adversarial = append(in.Adversarial, retry(first, 2))
	s := mustSummarize(t, in)
	wantEscapes(t, s, 0)
	if got := caseOf(t, s, "adversarial", "adv-000"); got.Disposition != string(Unsupported) {
		t.Fatalf("case = %+v, want UNSUPPORTED", got)
	}

	// An unregistered c_effect value is not an observed effect either.
	in = cleanInput()
	first = in.Adversarial[0]
	first.CEffect = "yes"
	in.Adversarial[0] = first
	in.Adversarial = append(in.Adversarial, retry(first, 2))
	wantEscapes(t, mustSummarize(t, in), 0)
}

func TestEscapeInEarlierAttemptUsesTerminalAttemptBitsForSubsetCheck(t *testing.T) {
	// The Escape Rate denominator is the terminal-attempt union. An escape whose
	// terminal attempt shows no attempt is outside that union, so the rate is
	// withheld, exactly as for a single attempt without attempt bits.
	in := cleanInput()
	first := invalidObserved(in.Adversarial[298]) // no attempt bits
	in.Adversarial[298] = first
	in.Adversarial = append(in.Adversarial, retry(first, 2))
	s := mustSummarize(t, in)
	wantEscapes(t, s, 1)
	if s.Metrics.EscapesSubsetOfAttempts {
		t.Fatal("an escape with no attempt bits must fail the subset check")
	}
}

func TestObservedEscapeInValidNonOutageEarlierAttemptStillRefused(t *testing.T) {
	in := cleanInput()
	first := in.Adversarial[0]
	first.CEffect = CEffectObserved
	in.Adversarial[0] = first
	in.Adversarial = append(in.Adversarial, retry(first, 2))
	if _, err := Summarize(in); err == nil {
		t.Fatal("want error: a valid observed escape cannot be superseded by a clean retry")
	}
}
