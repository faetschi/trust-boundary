package evalcorpus

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestThesisQuotasMatchPreregistration(t *testing.T) {
	q := ThesisQuotas()
	if q.Adversarial[ClassTMA] != 100 || q.Adversarial[ClassTMB] != 100 || q.Adversarial[ClassTMC] != 99 {
		t.Errorf("adversarial quotas = %v; want TM-A=100 TM-B=100 TM-C=99", q.Adversarial)
	}
	total := 0
	for _, n := range q.Adversarial {
		total += n
	}
	if total != 299 {
		t.Errorf("adversarial total = %d; want 299", total)
	}
	for _, tool := range []Tool{ToolRead, ToolWrite, ToolEdit, ToolBash} {
		if q.Compliant[tool] != 25 {
			t.Errorf("compliant quota for %s = %d; want 25", tool, q.Compliant[tool])
		}
	}
	if q.Ambiguous != 20 {
		t.Errorf("ambiguous quota = %d; want 20", q.Ambiguous)
	}
	if q.Variance[ClassTMA] != 17 || q.Variance[ClassTMB] != 17 || q.Variance[ClassTMC] != 16 {
		t.Errorf("variance quotas = %v; want TM-A=17 TM-B=17 TM-C=16", q.Variance)
	}
	if q.ColdPercent != 20 {
		t.Errorf("cold percent = %d; want 20", q.ColdPercent)
	}
	// floor(0.20*n + 0.5) per class: 100 -> 20, 100 -> 20, 99 -> 20.
	for _, class := range []ThreatClass{ClassTMA, ClassTMB, ClassTMC} {
		if got := ColdCount(q.ColdPercent, q.Adversarial[class]); got != 20 {
			t.Errorf("cold count for %s = %d; want 20", class, got)
		}
	}
}

func TestValidateAcceptsAllocatedSyntheticManifest(t *testing.T) {
	q := smallQuotas()
	wantClean(t, Validate(allocated(t, synthManifest(q, synthSeed), q), q))
}

func TestValidateReportsEveryViolation(t *testing.T) {
	q := smallQuotas()
	m := allocated(t, synthManifest(q, synthSeed), q)

	// Each statement below injects one fault. Every rule code in the list must be reported.
	findCase(t, &m, "SYNTH-ADV-TM-A-002").CaseID = "SYNTH-ADV-TM-A-001"
	findCase(t, &m, "SYNTH-ADV-TM-B-001").Adversarial.WorkspaceFixtureDigest = "sha256:XYZ"
	findCase(t, &m, "SYNTH-ADV-TM-C-001").Signoff.Reviewer = "another-advisor"
	findCase(t, &m, "SYNTH-ADV-TM-A-003").Adversarial.Adaptation.PreservedIntent = ""
	findCase(t, &m, "SYNTH-CMP-read-01").Compliant.PrimaryTool = ToolBash
	// The last case is an ambiguous one, so this drops one ambiguous case.
	m.Cases = m.Cases[:len(m.Cases)-1]

	vs := Validate(m, q)
	for _, rule := range []string{
		"case.id.duplicate", "digest.format", "review.identity", "field.empty",
		"count.compliant.read", "count.compliant.bash", "count.ambiguous", "count.total",
	} {
		wantRule(t, vs, rule)
	}
}

func TestValidateUniquenessRules(t *testing.T) {
	q := smallQuotas()
	t.Run("root scenario repeated across classes", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		root := findCase(t, &m, "SYNTH-ADV-TM-A-001").Adversarial.RootScenarioID
		findCase(t, &m, "SYNTH-ADV-TM-B-001").Adversarial.RootScenarioID = root
		wantRule(t, Validate(m, q), "adv.root.duplicate")
	})
	t.Run("executable unit repeated", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		a := findCase(t, &m, "SYNTH-ADV-TM-A-001").Adversarial
		b := findCase(t, &m, "SYNTH-ADV-TM-A-002").Adversarial
		b.WorkspaceFixtureDigest = a.WorkspaceFixtureDigest
		b.CalibrationPayloadDigest = a.CalibrationPayloadDigest
		wantRule(t, Validate(m, q), "adv.unit.duplicate")
	})
}

func TestValidateDigestFormat(t *testing.T) {
	q := smallQuotas()
	bad := map[string]string{
		"missing prefix":  strings.Repeat("a", 64),
		"uppercase hex":   "sha256:" + strings.Repeat("A", 64),
		"short":           "sha256:" + strings.Repeat("a", 63),
		"other algorithm": "sha512:" + strings.Repeat("a", 64),
	}
	for name, value := range bad {
		t.Run(name, func(t *testing.T) {
			m := allocated(t, synthManifest(q, synthSeed), q)
			findCase(t, &m, "SYNTH-AMB-01").Ambiguous.Rubric.Digest = value
			m.Pins.ModelDigest = value
			wantRule(t, Validate(m, q), "digest.format")
		})
	}
}

func TestValidateAmbiguousFixtureDigest(t *testing.T) {
	q := smallQuotas()
	t.Run("missing", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-AMB-01").Ambiguous.WorkspaceFixtureDigest = ""
		wantRule(t, Validate(m, q), "field.empty")
	})
	t.Run("malformed", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-AMB-01").Ambiguous.WorkspaceFixtureDigest = "sha256:" + strings.Repeat("A", 64)
		wantRule(t, Validate(m, q), "digest.format")
	})
	t.Run("changes the manifest digest", func(t *testing.T) {
		a := allocated(t, synthManifest(q, synthSeed), q)
		b := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &b, "SYNTH-AMB-01").Ambiguous.WorkspaceFixtureDigest = synthDigest("another fixture")
		da, _ := Digest(a)
		db, _ := Digest(b)
		if da == db {
			t.Error("changing an ambiguous fixture digest did not change the manifest digest")
		}
	})
}

func TestValidateColdCountRule(t *testing.T) {
	q := smallQuotas()
	m := allocated(t, synthManifest(q, synthSeed), q)
	nonCold := idsWhere(m, ClassTMA, func(a *Adversarial) bool { return a.Partition == PartitionNonCold && !a.VarianceSubset })
	findCase(t, &m, nonCold[0]).Adversarial.Partition = PartitionCold
	wantRule(t, Validate(m, q), "cold.count.TM-A")
}

func TestValidateColdSeedIsReproduced(t *testing.T) {
	q := smallQuotas()
	m := allocated(t, synthManifest(q, synthSeed), q)
	cold := idsWhere(m, ClassTMA, func(a *Adversarial) bool { return a.Partition == PartitionCold })
	nonCold := idsWhere(m, ClassTMA, func(a *Adversarial) bool { return a.Partition == PartitionNonCold && !a.VarianceSubset })
	if len(cold) != 1 || len(nonCold) == 0 {
		t.Fatalf("fixture: cold=%v nonCold=%v", cold, nonCold)
	}
	// Swap one cold and one non-cold TM-A case: the count is preserved, the seeded set is not.
	findCase(t, &m, cold[0]).Adversarial.Partition = PartitionNonCold
	findCase(t, &m, nonCold[0]).Adversarial.Partition = PartitionCold
	vs := Validate(m, q)
	wantRule(t, vs, "cold.seed.TM-A")
	wantNoRule(t, vs, "cold.count.TM-A")
}

func TestValidateVarianceRules(t *testing.T) {
	q := smallQuotas()
	t.Run("variance on a cold case", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		cold := idsWhere(m, ClassTMB, func(a *Adversarial) bool { return a.Partition == PartitionCold })
		findCase(t, &m, cold[0]).Adversarial.VarianceSubset = true
		wantRule(t, Validate(m, q), "adv.variance.partition")
	})
	t.Run("variance count", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		nonVariance := idsWhere(m, ClassTMA, func(a *Adversarial) bool { return a.Partition == PartitionNonCold && !a.VarianceSubset })
		findCase(t, &m, nonVariance[0]).Adversarial.VarianceSubset = true
		wantRule(t, Validate(m, q), "variance.count.TM-A")
	})
	t.Run("variance membership reproduced", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		member := idsWhere(m, ClassTMC, func(a *Adversarial) bool { return a.VarianceSubset })
		others := idsWhere(m, ClassTMC, func(a *Adversarial) bool { return a.Partition == PartitionNonCold && !a.VarianceSubset })
		if len(member) != 1 || len(others) != 1 {
			t.Fatalf("fixture: member=%v others=%v", member, others)
		}
		findCase(t, &m, member[0]).Adversarial.VarianceSubset = false
		findCase(t, &m, others[0]).Adversarial.VarianceSubset = true
		vs := Validate(m, q)
		wantRule(t, vs, "variance.seed.TM-C")
		wantNoRule(t, vs, "variance.count.TM-C")
	})
}

func TestValidateSignoffRules(t *testing.T) {
	q := smallQuotas()
	const target = "SYNTH-ADV-TM-A-001"
	cases := []struct {
		name   string
		mutate func(c *Case)
		want   string // rule expected; empty means the manifest must be clean
	}{
		{"not human", func(c *Case) { c.Signoff.Human = false }, "review.human"},
		{"signed by the case author", func(c *Case) { c.Signoff.Author = true }, "review.author"},
		{"empty reviewer", func(c *Case) { c.Signoff.Reviewer = "" }, "field.empty"},
		{"empty decision", func(c *Case) { c.Signoff.Decision = "" }, "field.empty"},
		{"another reviewer signed this case", func(c *Case) { c.Signoff.Reviewer = "another-advisor" }, "review.identity"},
		{"same reviewer after case and space normalization", func(c *Case) {
			c.Signoff.Reviewer = "  Synthetic-ADVISOR\t"
		}, ""},
		{"reviewers that differ only inside a word stay distinct", func(c *Case) {
			c.Signoff.Reviewer = "synthetic-advisor-10"
		}, "review.identity"},
		{"audited without a resolution", func(c *Case) { c.Signoff.Audited = true }, "review.resolution"},
		{"audited with a blank resolution", func(c *Case) {
			c.Signoff.Audited, c.Signoff.Resolution = true, " \t"
		}, "review.resolution"},
		{"audited with a placeholder resolution", func(c *Case) {
			c.Signoff.Audited, c.Signoff.Resolution = true, "TODO-RESOLUTION"
		}, "review.placeholder"},
		{"resolution without audited", func(c *Case) { c.Signoff.Resolution = "synthetic resolution" }, "review.audited"},
		{"audited with a resolution", func(c *Case) {
			c.Signoff.Audited, c.Signoff.Resolution = true, "synthetic resolution under the frozen rubric"
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := allocated(t, synthManifest(q, synthSeed), q)
			tc.mutate(findCase(t, &m, target))
			vs := validate(m, q)
			if tc.want == "" {
				wantClean(t, vs)
			} else {
				wantRule(t, vs, tc.want)
			}
		})
	}
}

// The two-review rule is gone: a manifest in that shape does not load.
func TestLoadRefusesTheRetiredReviewFields(t *testing.T) {
	q := smallQuotas()
	raw, err := json.Marshal(allocated(t, synthManifest(q, synthSeed), q))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(strings.NewReader(string(raw))); err != nil {
		t.Fatalf("Load of the current shape: %v", err)
	}
	for _, field := range []string{`"reviews":[]`, `"adjudication":{}`} {
		legacy := strings.Replace(string(raw), `"signoff":`, field+`,"signoff":`, 1)
		if _, err := Load(strings.NewReader(legacy)); err == nil {
			t.Errorf("Load accepted %s", field)
		}
	}
}

func TestValidateAdaptationEvidence(t *testing.T) {
	q := smallQuotas()
	const target = "SYNTH-ADV-TM-A-001"
	cases := []struct {
		name   string
		mutate func(a *Adversarial)
		want   string
	}{
		{"inherited label without equivalence evidence", func(a *Adversarial) {
			a.Adaptation.LabelOrigin = LabelSourceInherited
		}, "adaptation.evidence"},
		{"inherited label with equivalence evidence", func(a *Adversarial) {
			a.Adaptation.LabelOrigin = LabelSourceInherited
			a.Adaptation.EquivalenceEvidence = "synthetic equivalence note"
		}, ""},
		{"unknown label origin", func(a *Adversarial) {
			a.Adaptation.LabelOrigin = "guessed"
		}, "field.invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := allocated(t, synthManifest(q, synthSeed), q)
			tc.mutate(findCase(t, &m, target).Adversarial)
			vs := validate(m, q)
			if tc.want == "" {
				wantClean(t, vs)
			} else {
				wantRule(t, vs, tc.want)
			}
		})
	}
}

func TestValidateRequiredAdversarialFields(t *testing.T) {
	q := smallQuotas()
	const target = "SYNTH-ADV-TM-A-001"
	blank := map[string]func(a *Adversarial){
		"root_scenario_id":           func(a *Adversarial) { a.RootScenarioID = "" },
		"source_key":                 func(a *Adversarial) { a.SourceKey = "" },
		"source_item_id":             func(a *Adversarial) { a.SourceItemID = "" },
		"selection_reason":           func(a *Adversarial) { a.SelectionReason = "" },
		"preserved_intent":           func(a *Adversarial) { a.Adaptation.PreservedIntent = "" },
		"tool_mapping":               func(a *Adversarial) { a.Adaptation.ToolMapping = "" },
		"authorization_class":        func(a *Adversarial) { a.Adaptation.AuthorizationClass = "" },
		"endpoint_oracle_mapping":    func(a *Adversarial) { a.Adaptation.EndpointOracleMapping = "" },
		"oracle_id":                  func(a *Adversarial) { a.OracleID = "" },
		"oracle_version":             func(a *Adversarial) { a.OracleVersion = "" },
		"forbidden_effects":          func(a *Adversarial) { a.ForbiddenEffects = nil },
		"workspace_fixture_digest":   func(a *Adversarial) { a.WorkspaceFixtureDigest = "" },
		"calibration_payload_digest": func(a *Adversarial) { a.CalibrationPayloadDigest = "" },
		"calibration_witness_id":     func(a *Adversarial) { a.CalibrationWitnessID = "" },
		"opportunity_predicate":      func(a *Adversarial) { a.OpportunityPredicate = "" },
	}
	for name, mutate := range blank {
		t.Run(name, func(t *testing.T) {
			m := allocated(t, synthManifest(q, synthSeed), q)
			mutate(findCase(t, &m, target).Adversarial)
			wantRule(t, Validate(m, q), "field.empty")
		})
	}
}

func TestValidateEnumFields(t *testing.T) {
	q := smallQuotas()
	t.Run("threat class", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-ADV-TM-A-001").Adversarial.ThreatClass = "TM-D"
		wantRule(t, Validate(m, q), "field.invalid")
	})
	t.Run("partition", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-ADV-TM-A-001").Adversarial.Partition = "warm"
		wantRule(t, Validate(m, q), "field.invalid")
	})
	t.Run("opportunity tag", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-ADV-TM-A-001").Adversarial.OpportunityTag = "maybe"
		wantRule(t, Validate(m, q), "field.invalid")
	})
	t.Run("primary tool", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-CMP-read-01").Compliant.PrimaryTool = "shell"
		wantRule(t, Validate(m, q), "field.invalid")
	})
}

func TestValidateKindMatchesPayload(t *testing.T) {
	q := smallQuotas()
	t.Run("compliant kind with adversarial payload", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-ADV-TM-A-001").Kind = KindCompliant
		wantRule(t, Validate(m, q), "case.payload")
	})
	t.Run("adversarial kind without payload", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-ADV-TM-A-001").Adversarial = nil
		wantRule(t, Validate(m, q), "case.payload")
	})
	t.Run("unknown kind", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		findCase(t, &m, "SYNTH-ADV-TM-A-001").Kind = "bogus"
		wantRule(t, Validate(m, q), "case.kind")
	})
}

func TestValidateCompliantRules(t *testing.T) {
	q := smallQuotas()
	const target = "SYNTH-CMP-read-01"
	cases := []struct {
		name   string
		mutate func(c *Compliant)
		want   string
	}{
		{"reference solution requires a forbidden effect", func(c *Compliant) {
			c.ReferenceSolution.ForbiddenEffects = []string{"SYNTH-EFF-01"}
		}, "cmp.reference.forbidden_effect"},
		{"acceptance test reference missing", func(c *Compliant) { c.AcceptanceTest.Ref = "" }, "field.empty"},
		{"acceptance test digest missing", func(c *Compliant) { c.AcceptanceTest.Digest = "" }, "field.empty"},
		{"task goal missing", func(c *Compliant) { c.TaskGoal = "" }, "field.empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := allocated(t, synthManifest(q, synthSeed), q)
			tc.mutate(findCase(t, &m, target).Compliant)
			wantRule(t, Validate(m, q), tc.want)
		})
	}
}

func TestValidateSchemaSeedAndQuotas(t *testing.T) {
	q := smallQuotas()
	t.Run("schema version", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		m.SchemaVersion = "tbound-evalcorpus/v0"
		wantRule(t, Validate(m, q), "schema.version")
	})
	t.Run("empty seed", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		m.SplitSeed = ""
		wantRule(t, Validate(m, q), "seed.empty")
	})
	t.Run("cold percent out of range", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		bad := smallQuotas()
		bad.ColdPercent = 120
		wantRule(t, Validate(m, bad), "quota.invalid")
	})
	t.Run("quota keys incomplete", func(t *testing.T) {
		m := allocated(t, synthManifest(q, synthSeed), q)
		bad := smallQuotas()
		bad.Compliant = map[Tool]int{ToolRead: 2}
		wantRule(t, Validate(m, bad), "quota.invalid")
	})
}

// PilotQuotas are the explicit counts of a pilot or G4 dry-run corpus. They are not the
// registered quotas, and a pilot corpus is not evidence for any claim.
func TestPilotQuotasAreSmallExplicitAndDistinctFromThesis(t *testing.T) {
	q, thesis := PilotQuotas(), ThesisQuotas()
	if err := q.check(); err != nil {
		t.Fatal(err)
	}
	if q.Adversarial[ClassTMA] != 7 || q.Adversarial[ClassTMB] != 4 || q.Adversarial[ClassTMC] != 0 ||
		q.Compliant[ToolRead] != 5 || q.Compliant[ToolWrite] != 5 || q.Compliant[ToolEdit] != 5 || q.Compliant[ToolBash] != 5 ||
		q.Ambiguous != 2 || q.Variance[ClassTMA] != 1 || q.Variance[ClassTMB] != 1 || q.Variance[ClassTMC] != 0 ||
		q.ColdPercent != 20 {
		t.Errorf("pilot quotas drifted: %+v", q)
	}
	m := allocated(t, synthManifest(q, synthSeed), q)
	wantClean(t, Validate(m, q))
	wantRule(t, Validate(m, thesis), "count.total")
	pilotAsThesis := allocated(t, synthManifest(thesis, synthSeed), thesis)
	wantRule(t, Validate(pilotAsThesis, q), "count.total")
}

func TestValidateSignoffDecisionGate(t *testing.T) {
	q := smallQuotas()
	const target = "SYNTH-ADV-TM-B-002"
	cases := []struct {
		name   string
		mutate func(c *Case)
		want   []string // rules that must be reported
		not    []string // rules that must not be reported
	}{
		{"accept", func(c *Case) {}, nil, []string{"review.rejected", "review.decision"}},
		{"reject", func(c *Case) { c.Signoff.Decision = "reject" }, []string{"review.rejected"}, []string{"review.decision"}},
		{"free-text decision", func(c *Case) { c.Signoff.Decision = "synthetic-label" }, []string{"review.decision"}, nil},
		{"the audit sheet revise is not a manifest decision", func(c *Case) { c.Signoff.Decision = "revise" }, []string{"review.decision"}, nil},
		{"decision with different case", func(c *Case) { c.Signoff.Decision = "Accept" }, []string{"review.decision"}, nil},
		{"decision with surrounding space", func(c *Case) { c.Signoff.Decision = "accept " }, []string{"review.decision"}, nil},
		{"placeholder decision", func(c *Case) { c.Signoff.Decision = "TODO-DECISION" }, []string{"review.decision"}, nil},
		{"TODO reviewer", func(c *Case) { c.Signoff.Reviewer = "  todo-reviewer-advisor" }, []string{"review.placeholder"}, nil},
		{"reviewer that merely contains todo", func(c *Case) { c.Signoff.Reviewer = "methodology-todo-lead" }, nil, []string{"review.placeholder"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := allocated(t, synthManifest(q, synthSeed), q)
			tc.mutate(findCase(t, &m, target))
			vs := validate(m, q)
			for _, rule := range tc.want {
				wantRule(t, vs, rule)
				for _, v := range vs {
					if v.Rule == rule && !strings.Contains(v.Detail, target) {
						t.Errorf("violation %s does not name the case %q: %s", rule, target, v.Detail)
					}
				}
			}
			for _, rule := range tc.not {
				wantNoRule(t, vs, rule)
			}
		})
	}
}

// ConstructReview binds the manifest to the content the panel rated and to the panel files.
func TestValidateConstructReview(t *testing.T) {
	q := smallQuotas()
	good := allocated(t, synthManifest(q, synthSeed), q)
	wantClean(t, Validate(good, q))

	t.Run("a mutation after sealing makes the content digest stale", func(t *testing.T) {
		m := good
		m.Cases = append([]Case(nil), good.Cases...)
		adv := *good.Cases[0].Adversarial
		adv.SelectionReason = "edited after the panel rated it"
		m.Cases[0].Adversarial = &adv
		want, err := ContentDigest(m)
		if err != nil {
			t.Fatal(err)
		}
		vs := Validate(m, q)
		wantRule(t, vs, "review.content_digest")
		for _, v := range vs {
			if v.Rule == "review.content_digest" && !strings.Contains(v.Detail, want) {
				t.Errorf("violation does not name the content digest %s: %s", want, v.Detail)
			}
		}
	})
	t.Run("a signoff or panel digest change keeps the content digest valid", func(t *testing.T) {
		m := good
		m.Cases = append([]Case(nil), good.Cases...)
		m.Cases[0].Signoff = Signoff{Reviewer: "synthetic-advisor", Human: true, Decision: "accept", Audited: true, Resolution: "ok"}
		m.ConstructReview.PanelAuditDigest = synthDigest("another audit")
		wantClean(t, Validate(m, q))
	})
	fields := []struct {
		name string
		set  func(r *ConstructReview, v string)
		want map[string]string // value -> rule
	}{
		{"content digest", func(r *ConstructReview, v string) { r.ContentDigest = v }, map[string]string{
			"": "review.content_digest", "TODO-DIGEST": "review.content_digest", "sha256:XYZ": "review.content_digest"}},
		{"panel receipt digest", func(r *ConstructReview, v string) { r.PanelReceiptDigest = v }, map[string]string{
			"": "review.digest", "TODO-DIGEST": "review.placeholder", "sha256:XYZ": "review.digest",
			"sha256:" + strings.Repeat("A", 64): "review.digest"}},
		{"panel stats digest", func(r *ConstructReview, v string) { r.PanelStatsDigest = v }, map[string]string{
			"": "review.digest", "TODO-DIGEST": "review.placeholder", "sha256:XYZ": "review.digest", "sha256:" + strings.Repeat("A", 64): "review.digest"}},
		{"panel audit digest", func(r *ConstructReview, v string) { r.PanelAuditDigest = v }, map[string]string{
			"": "review.digest", "TODO-DIGEST": "review.placeholder", "sha256:XYZ": "review.digest",
			"sha256:" + strings.Repeat("A", 64): "review.digest"}},
	}
	for _, f := range fields {
		for value, rule := range f.want {
			t.Run(f.name+" "+value, func(t *testing.T) {
				m := good
				f.set(&m.ConstructReview, value)
				wantRule(t, Validate(m, q), rule)
			})
		}
	}
}

// A draft manifest whose sign-offs are still placeholders must stay non-admissible even
// after someone sets human, which is the only field a hand edit flips first.
func TestValidateDraftPilotManifestWithHumanSetIsStillRefused(t *testing.T) {
	f, err := os.Open("../../../corpus/pilot/manifest.draft.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Cases {
		m.Cases[i].Signoff.Human = true
	}
	vs := Validate(m, PilotQuotas())
	wantRule(t, vs, "review.placeholder")
	wantRule(t, vs, "review.decision")
	wantRule(t, vs, "review.content_digest")
	wantNoRule(t, vs, "review.human")
}

// A valid D37 meaning record attached to a case passes; a case with no record stays
// valid; and each missing or malformed field is reported under a meaning.* or shared rule.
func TestValidateMeaningRecordGate(t *testing.T) {
	q := smallQuotas()
	const target = "SYNTH-ADV-TM-A-001"
	good := func() *MeaningRecord {
		return &MeaningRecord{
			CaseOrigin: OriginEstablished, BenchmarkKey: "InjecAgent", BenchmarkRevision: "f19c9f2",
			BenchmarkDigest: synthDigest("item"), SourceItemID: "dh:line:10", RootID: "InjecAgent:dh:line:10",
			License: "MIT", Redistribution: "notice retained", PreservedIntent: "move a protected file away",
			TaskUnit: "single injected instruction", AuthorizationClass: "injected-third-party-instruction",
			Transformations: []string{"service tool mapped to bash mv"}, LostDistinctions: []string{},
			ToolMapping: "Pi bash mv", OracleMapping: "EFF-WS-02 removal", LabelOrigin: LabelExternalOracle,
		}
	}
	base := allocated(t, synthManifest(q, synthSeed), q)
	findCase(t, &base, target).MeaningRecord = good()
	wantClean(t, validate(base, q))

	cases := []struct {
		name   string
		mutate func(mr *MeaningRecord)
		rule   string
	}{
		{"bad origin", func(mr *MeaningRecord) { mr.CaseOrigin = "authored" }, "meaning.origin"},
		{"empty benchmark key", func(mr *MeaningRecord) { mr.BenchmarkKey = "" }, "field.empty"},
		{"bad benchmark digest", func(mr *MeaningRecord) { mr.BenchmarkDigest = "sha256:zz" }, "digest.format"},
		{"no transformations", func(mr *MeaningRecord) { mr.Transformations = nil }, "meaning.field"},
		{"bad label origin", func(mr *MeaningRecord) { mr.LabelOrigin = "guess" }, "meaning.field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := allocated(t, synthManifest(q, synthSeed), q)
			mr := good()
			tc.mutate(mr)
			findCase(t, &m, target).MeaningRecord = mr
			wantRule(t, Validate(m, q), tc.rule)
		})
	}
}
