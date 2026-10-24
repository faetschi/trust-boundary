package effectoracle

import "testing"

func fullWindow() *Report {
	return &Report{
		OracleVersion: OracleVersion, Instrument: Instrument, Verdict: VerdictOK,
		Heartbeats: []Heartbeat{
			{Seq: 1, Label: "mark:start:pre", Observed: true}, {Seq: 2, Label: "mark:start:post", Observed: true},
			{Seq: 4, Label: "mark:stop:pre", Observed: true}, {Seq: 5, Label: "mark:stop:post", Observed: true},
		},
		Marks: []Mark{{Seq: 3, Name: MarkStart, Quiescent: true}, {Seq: 6, Name: MarkStop, Quiescent: true}},
	}
}

func TestQualifiedWindowNeedsTheWholeWindowInTheRecords(t *testing.T) {
	if !fullWindow().QualifiedWindow() {
		t.Fatal("a complete window is not qualified")
	}
	for name, mutate := range map[string]func(*Report){
		"empty report":           func(r *Report) { *r = Report{Verdict: VerdictOK} },
		"no start mark":          func(r *Report) { r.Marks = r.Marks[1:] },
		"no stop mark":           func(r *Report) { r.Marks = r.Marks[:1] },
		"duplicate stop":         func(r *Report) { r.Marks = append(r.Marks, Mark{Seq: 7, Name: MarkStop}) },
		"stop before start":      func(r *Report) { r.Marks[0].Seq, r.Marks[1].Seq = 6, 3 },
		"a heartbeat is missing": func(r *Report) { r.Heartbeats = r.Heartbeats[:3] },
		"a heartbeat is twice":   func(r *Report) { r.Heartbeats = append(r.Heartbeats, r.Heartbeats[0]) },
		"a heartbeat unobserved": func(r *Report) { r.Heartbeats[3].Observed = false },
		"a gap under verdict ok": func(r *Report) { r.Gaps = []Gap{{Seq: 9, Kind: GapOverflow}} },
		"verdict edited":         func(r *Report) { r.Verdict = VerdictEvidenceLost },
		"verdict empty":          func(r *Report) { r.Verdict = "" },
	} {
		r := fullWindow()
		mutate(r)
		if r.QualifiedWindow() {
			t.Errorf("%s: qualified", name)
		}
	}
}
