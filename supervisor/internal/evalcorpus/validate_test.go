package evalcorpus

import (
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
	findCase(t, &m, "SYNTH-ADV-TM-C-001").Reviews[1].Role = "reviewer-a"
	findCase(t, &m, "SYNTH-ADV-TM-A-003").Adversarial.Adaptation.PreservedIntent = ""
	findCase(t, &m, "SYNTH-CMP-read-01").Compliant.PrimaryTool = ToolBash
	// The last case is an ambiguous one, so this drops one ambiguous case.
	m.Cases = m.Cases[:len(m.Cases)-1]

	vs := Validate(m, q)
	for _, rule := range []string{
		"case.id.duplicate", "digest.format", "review.role", "field.empty",
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

func TestValidateReviewRules(t *testing.T) {
	q := smallQuotas()
	const target = "SYNTH-ADV-TM-A-001"
	cases := []struct {
		name   string
		mutate func(c *Case)
		want   string // rule expected; empty means the manifest must be clean
	}{
		{"one review", func(c *Case) { c.Reviews = c.Reviews[:1] }, "review.count"},
		{"same role", func(c *Case) { c.Reviews[1].Role = c.Reviews[0].Role }, "review.role"},
		{"same identity", func(c *Case) { c.Reviews[1].Reviewer = c.Reviews[0].Reviewer }, "review.identity"},
		{"not human", func(c *Case) { c.Reviews[0].Human = false }, "review.human"},
		{"not blinded", func(c *Case) { c.Reviews[1].Blinded = false }, "review.blinded"},
		{"empty decision", func(c *Case) { c.Reviews[0].Decision = "" }, "field.empty"},
		{"disagreement without adjudication", func(c *Case) { c.Reviews[1].Decision = "other" }, "adjudication.missing"},
		{"adjudication without disagreement", func(c *Case) {
			c.Adjudication = &Adjudication{Adjudicator: "synthetic-adjudicator", Human: true, Blinded: true, Decision: "synthetic-label"}
		}, "adjudication.unexpected"},
		{"adjudicator is a reviewer", func(c *Case) {
			c.Reviews[1].Decision = "other"
			c.Adjudication = &Adjudication{Adjudicator: "synthetic-reviewer-1", Human: true, Blinded: true, Decision: "other"}
		}, "adjudication.not_third"},
		{"adjudicator is not human", func(c *Case) {
			c.Reviews[1].Decision = "other"
			c.Adjudication = &Adjudication{Adjudicator: "synthetic-adjudicator", Human: false, Blinded: true, Decision: "other"}
		}, "adjudication.human"},
		{"adjudicator is not blinded", func(c *Case) {
			c.Reviews[1].Decision = "other"
			c.Adjudication = &Adjudication{Adjudicator: "synthetic-adjudicator", Human: true, Blinded: false, Decision: "other"}
		}, "adjudication.blinded"},
		{"disagreement resolved by a third reviewer", func(c *Case) {
			c.Reviews[1].Decision = "other"
			c.Adjudication = &Adjudication{Adjudicator: "synthetic-adjudicator", Human: true, Blinded: true, Decision: "other"}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := allocated(t, synthManifest(q, synthSeed), q)
			tc.mutate(findCase(t, &m, target))
			vs := Validate(m, q)
			if tc.want == "" {
				wantClean(t, vs)
			} else {
				wantRule(t, vs, tc.want)
			}
		})
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
			vs := Validate(m, q)
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
