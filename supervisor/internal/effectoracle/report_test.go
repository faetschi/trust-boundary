package effectoracle

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// markAt builds a mark record for report-level tests that do not touch the kernel.
func markAt(name string, seq uint64, entries ...Entry) Mark {
	return Mark{Seq: seq, Name: name, Quiescent: true, Entries: entries}
}

// fileAt builds a regular-file entry with a fixed digest.
func fileAt(path, digest string) Entry {
	return Entry{Path: path, Type: "file", Mode: 0o600, UID: 1000, GID: 1000, Nlink: 1, Size: 1, SHA256: digest}
}

func TestTransientCreateThenDeleteIsFoundFromEventsAlone(t *testing.T) {
	r := &Report{
		Events: []Event{
			{Seq: 2, Kind: KindCreate, Path: "f.txt"},
			{Seq: 3, Kind: KindModify, Path: "f.txt"},
			{Seq: 4, Kind: KindDelete, Path: "f.txt"},
		},
		Marks: []Mark{markAt(MarkStart, 1), markAt(MarkStop, 5)},
	}
	want := []Transient{{
		Kind:            TransientCreatedThenDeleted,
		Path:            "f.txt",
		EventSeqs:       []uint64{2, 4},
		ObservedAtMarks: []string{},
	}}
	if got := r.TransientMutations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TransientMutations() = %+v, want %+v", got, want)
	}
}

func TestTransientCreateThenDeleteRecordsMarksThatSawIt(t *testing.T) {
	r := &Report{
		Events: []Event{
			{Seq: 2, Kind: KindCreate, Path: "f.txt"},
			{Seq: 4, Kind: KindDelete, Path: "f.txt"},
		},
		Marks: []Mark{
			markAt(MarkStart, 1),
			markAt("mid", 3, fileAt("f.txt", "aa")),
			markAt(MarkStop, 5),
		},
	}
	got := r.TransientMutations()
	if len(got) != 1 || !reflect.DeepEqual(got[0].ObservedAtMarks, []string{"mid"}) {
		t.Fatalf("TransientMutations() = %+v, want one candidate observed at mid", got)
	}
}

func TestTransientRevertedFindsEqualEndpointsWithAnIntermediateChange(t *testing.T) {
	r := &Report{
		Events: []Event{{Seq: 2, Kind: KindCloseWrite, Path: "f.txt"}},
		Marks: []Mark{
			markAt(MarkStart, 1, fileAt("f.txt", "aa")),
			markAt("mid", 3, fileAt("f.txt", "bb")),
			markAt(MarkStop, 5, fileAt("f.txt", "aa")),
		},
	}
	want := []Transient{{
		Kind:            TransientReverted,
		Path:            "f.txt",
		EventSeqs:       []uint64{2},
		ObservedAtMarks: []string{"mid"},
	}}
	if got := r.TransientMutations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TransientMutations() = %+v, want %+v", got, want)
	}
}

func TestDeleteAndRestoreWithoutAnObservedIntermediateIsTouchedUnchanged(t *testing.T) {
	r := &Report{
		Events: []Event{
			{Seq: 2, Kind: KindDelete, Path: "f.txt"},
			{Seq: 3, Kind: KindCreate, Path: "f.txt"},
		},
		Marks: []Mark{markAt(MarkStart, 1, fileAt("f.txt", "aa")), markAt(MarkStop, 4, fileAt("f.txt", "aa"))},
	}
	// No mark saw the path between the delete and the restore, so the
	// intermediate state is unknown and the endpoints alone cannot show a revert.
	want := []Transient{{
		Kind:            TransientTouchedUnchanged,
		Path:            "f.txt",
		EventSeqs:       []uint64{2, 3},
		ObservedAtMarks: []string{},
	}}
	if got := r.TransientMutations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TransientMutations() = %+v, want %+v", got, want)
	}
}

func TestDeleteAndRestoreObservedAtAMarkIsReverted(t *testing.T) {
	r := &Report{
		Events: []Event{
			{Seq: 2, Kind: KindDelete, Path: "f.txt"},
			{Seq: 4, Kind: KindCreate, Path: "f.txt"},
		},
		Marks: []Mark{
			markAt(MarkStart, 1, fileAt("f.txt", "aa")),
			markAt("gone", 3),
			markAt(MarkStop, 5, fileAt("f.txt", "aa")),
		},
	}
	want := []Transient{{
		Kind:            TransientReverted,
		Path:            "f.txt",
		EventSeqs:       []uint64{2, 4},
		ObservedAtMarks: []string{"gone"},
	}}
	if got := r.TransientMutations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TransientMutations() = %+v, want %+v", got, want)
	}
}

func TestPathWithoutAnObservedDifferenceIsTouchedUnchanged(t *testing.T) {
	endpoints := []Mark{markAt(MarkStart, 1, fileAt("f.txt", "aa")), markAt(MarkStop, 5, fileAt("f.txt", "aa"))}
	cases := []struct {
		name   string
		events []Event
		marks  []Mark
		seqs   []uint64
	}{
		{
			name:   "same-mode chmod",
			events: []Event{{Seq: 2, Kind: KindAttrib, Path: "f.txt"}},
			marks:  endpoints,
			seqs:   []uint64{2},
		},
		{
			name: "same-bytes rewrite",
			events: []Event{
				{Seq: 2, Kind: KindModify, Path: "f.txt"},
				{Seq: 3, Kind: KindCloseWrite, Path: "f.txt"},
			},
			marks: endpoints,
			seqs:  []uint64{2, 3},
		},
		{
			name: "same-bytes rewrite with an identical mark between",
			events: []Event{
				{Seq: 2, Kind: KindModify, Path: "f.txt"},
				{Seq: 4, Kind: KindCloseWrite, Path: "f.txt"},
			},
			marks: []Mark{
				markAt(MarkStart, 1, fileAt("f.txt", "aa")),
				markAt("mid", 3, fileAt("f.txt", "aa")),
				markAt(MarkStop, 5, fileAt("f.txt", "aa")),
			},
			seqs: []uint64{2, 4},
		},
	}
	for _, c := range cases {
		r := &Report{Events: c.events, Marks: c.marks}
		want := []Transient{{
			Kind:            TransientTouchedUnchanged,
			Path:            "f.txt",
			EventSeqs:       c.seqs,
			ObservedAtMarks: []string{},
		}}
		if got := r.TransientMutations(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: TransientMutations() = %+v, want %+v", c.name, got, want)
		}
	}
}

func TestRetainedChangeIsNotTransient(t *testing.T) {
	r := &Report{
		Events: []Event{{Seq: 2, Kind: KindCloseWrite, Path: "f.txt"}},
		Marks:  []Mark{markAt(MarkStart, 1, fileAt("f.txt", "aa")), markAt(MarkStop, 3, fileAt("f.txt", "bb"))},
	}
	if got := r.TransientMutations(); len(got) != 0 {
		t.Fatalf("a retained endpoint difference must not be reported as transient, got %+v", got)
	}
}

func TestEqualEndpointsWithoutEventsAreNotTransient(t *testing.T) {
	r := &Report{
		Marks: []Mark{markAt(MarkStart, 1, fileAt("f.txt", "aa")), markAt(MarkStop, 2, fileAt("f.txt", "aa"))},
	}
	if got := r.TransientMutations(); len(got) != 0 {
		t.Fatalf("no recorded mutation means no transient candidate, got %+v", got)
	}
}

func TestTransientMutationsAreOrderedBySequenceNotByName(t *testing.T) {
	r := &Report{
		Events: []Event{
			{Seq: 2, Kind: KindCreate, Path: "z.txt"},
			{Seq: 3, Kind: KindDelete, Path: "z.txt"},
			{Seq: 4, Kind: KindCreate, Path: "a.txt"},
			{Seq: 5, Kind: KindDelete, Path: "a.txt"},
		},
		Marks: []Mark{markAt(MarkStart, 1), markAt(MarkStop, 6)},
	}
	got := r.TransientMutations()
	if len(got) != 2 || got[0].Path != "z.txt" || got[1].Path != "a.txt" {
		t.Fatalf("TransientMutations() paths out of sequence order: %+v", got)
	}
}

// sampleReport is a small complete report whose only wall-clock input is atNS.
func sampleReport(atNS int64, path string) *Report {
	return &Report{
		OracleVersion: OracleVersion,
		Instrument:    Instrument,
		Masks:         []string{"IN_CREATE"},
		Verdict:       VerdictOK,
		Preservation:  PreservationFields,
		Events:        []Event{{Seq: 2, AtNS: atNS, Kind: KindCreate, Path: path}},
		Marks:         []Mark{markAt(MarkStart, 1)},
	}
}

func TestJSONIsDeterministicEncodesEmptyListsAndRoundTrips(t *testing.T) {
	var empty Report
	js, err := empty.JSON()
	if err != nil {
		t.Fatalf("JSON() of zero report: %v", err)
	}
	for _, want := range []string{`"events": []`, `"gaps": []`, `"heartbeats": []`, `"marks": []`, `"masks": []`} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("zero report JSON lacks %s:\n%s", want, js)
		}
	}
	r := sampleReport(7, "f.txt")
	first, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON(): %v", err)
	}
	second, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON() again: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("two encodings of one report differ")
	}
	var back Report
	if err := json.Unmarshal(first, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	again, err := back.JSON()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if string(again) != string(first) {
		t.Fatalf("JSON does not round-trip:\n%s\n---\n%s", first, again)
	}
}

func TestDigestIgnoresWallClockButCoversContent(t *testing.T) {
	early := sampleReport(7, "f.txt")
	late := sampleReport(9999, "f.txt")
	dEarly, err := early.Digest()
	if err != nil {
		t.Fatalf("Digest(): %v", err)
	}
	dLate, err := late.Digest()
	if err != nil {
		t.Fatalf("Digest() of late report: %v", err)
	}
	if dEarly != dLate {
		t.Fatalf("digest depends on at_ns: %s vs %s", dEarly, dLate)
	}
	if len(dEarly) != 64 {
		t.Fatalf("digest %q is not a hex SHA-256", dEarly)
	}
	jsEarly, _ := early.JSON()
	jsLate, _ := late.JSON()
	if string(jsEarly) == string(jsLate) {
		t.Fatal("JSON must keep at_ns even though the digest excludes it")
	}
	dOther, err := sampleReport(7, "g.txt").Digest()
	if err != nil {
		t.Fatalf("Digest() of other report: %v", err)
	}
	if dOther == dEarly {
		t.Fatal("digest did not change when a recorded path changed")
	}
	if early.Events[0].AtNS != 7 {
		t.Fatal("Digest must not modify the report it hashes")
	}
}

func TestVerdictPrecedenceGapsOverHeartbeats(t *testing.T) {
	live := []Heartbeat{{Label: "start", Observed: true}}
	silent := []Heartbeat{{Label: "start", Observed: false}}
	loss := []Gap{{Kind: GapOverflow}}
	cases := []struct {
		name  string
		gaps  []Gap
		beats []Heartbeat
		want  string
	}{
		{"live and lossless", nil, live, VerdictOK},
		{"canary missing with no loss record", nil, silent, VerdictInstrumentUnqualified},
		{"loss record with live canaries", loss, live, VerdictEvidenceLost},
		{"loss record with a missing canary", loss, silent, VerdictEvidenceLost},
	}
	for _, c := range cases {
		if got := verdictFor(c.gaps, c.beats); got != c.want {
			t.Errorf("%s: verdictFor = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPreservationFieldsNameTheGapAndHeartbeatFields(t *testing.T) {
	for _, want := range []string{"gaps[].kind", "gaps[].path", "gaps[].from_seq", "gaps[].to_seq", "heartbeats[].observed"} {
		found := false
		for _, got := range PreservationFields {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("PreservationFields %v lacks %q", PreservationFields, want)
		}
	}
}

func TestOracleVersionIsV4ForTheRootListingRecords(t *testing.T) {
	if OracleVersion != "effectoracle/v4" {
		t.Fatalf("OracleVersion = %q: the fences gained the root's listings (issue #184)", OracleVersion)
	}
}

func renameReport(fenceAt int64, cookies ...uint32) *Report {
	r := sampleReport(1, "f.txt")
	r.Fences = []Fence{{Seq: 1, AtNS: fenceAt, Op: FenceOpen, Path: FenceDir, IsDir: true}}
	r.Events = nil
	for i, c := range cookies {
		kind := KindMovedTo
		if i%2 == 1 {
			kind = KindMovedFrom
		}
		r.Events = append(r.Events, Event{Seq: uint64(i + 2), Kind: kind, Path: "f.txt", Cookie: c})
	}
	return r
}

// The digest keeps which events share a cookie and not the kernel's cookie values, so identical workloads still have
// identical digests; the time of a fence is dropped like at_ns.
func TestDigestKeepsCookiePairingButNotCookieValues(t *testing.T) {
	digest := func(r *Report) string {
		d, err := r.Digest()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a, b := renameReport(5, 111, 222, 333, 444), renameReport(99999, 7, 8, 9, 10)
	if digest(a) != digest(b) {
		t.Error("the digest depends on cookie values or the time of a fence")
	}
	if digest(renameReport(5, 111, 111, 333, 444)) == digest(a) {
		t.Error("the digest does not tell a shared cookie from distinct ones")
	}
	if digest(renameReport(5, 0, 222, 333, 444)) == digest(a) {
		t.Error("the digest does not tell a missing cookie from a present one")
	}
	js, err := a.JSON()
	if err != nil || !strings.Contains(string(js), `"cookie": 111`) || !strings.Contains(string(js), `"at_ns": 5`) {
		t.Errorf("JSON drops the cookie or the fence: %v\n%s", err, js)
	}
}
