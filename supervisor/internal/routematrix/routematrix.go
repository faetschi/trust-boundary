package routematrix

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Route is one of the four E06 exposure routes.
type Route string

const (
	RouteDeclaredRead   Route = "declared-read"
	RouteBashView       Route = "bash-view"
	RouteDirectRuntime  Route = "direct-runtime"
	RouteProviderBroker Route = "provider-broker"
)

// Routes lists the four registered routes in spec order.
var Routes = []Route{RouteDeclaredRead, RouteBashView, RouteDirectRuntime, RouteProviderBroker}

// SensitiveClass names one sensitive fixture class. The spec does not name the
// classes, so they are caller-supplied input data.
type SensitiveClass string

// Treatment is how a route treats a sensitive class. The set allowed per route
// is the implementer's reading of E06; see allowedTreatments.
type Treatment string

const (
	TreatmentOmitted        Treatment = "omitted"
	TreatmentMarker         Treatment = "marker"
	TreatmentUnreachable    Treatment = "unreachable"
	TreatmentProposalDenied Treatment = "proposal-denied"
)

// EvidenceKind names one evidence type. E06 keeps these types separate.
type EvidenceKind string

const (
	// EvidenceAttemptedRead is a recorded attempt to read a sensitive target.
	EvidenceAttemptedRead EvidenceKind = "AttemptedRead"
	// EvidenceSuccessfulRead is a recorded read that succeeded.
	EvidenceSuccessfulRead EvidenceKind = "SuccessfulRead"
	// EvidenceProviderChannelDisclosure is a recorded classification of
	// disclosure on the provider channel.
	EvidenceProviderChannelDisclosure EvidenceKind = "ProviderChannelDisclosure"
)

// Status is the evidence status of one cell.
type Status string

const (
	StatusCovered      Status = "Covered"
	StatusPendingGuest Status = "Pending_Guest"
	StatusMissing      Status = "Missing"
)

// Evidence binds one evidence kind of a cell to the test that evidences it.
type Evidence struct {
	Kind    EvidenceKind
	TestRef string
}

// Cell is one (route, class) entry of the registered matrix.
type Cell struct {
	Route     Route
	Class     SensitiveClass
	Treatment Treatment
	Evidence  []Evidence
	Status    Status
	Reason    string
}

// Matrix is a registered exposure matrix. Routes are fixed by the spec; Classes
// is the caller-supplied class list that every route must cover exactly once.
type Matrix struct {
	Classes []SensitiveClass
	Cells   []Cell
}

// Report is the coverage summary of a valid matrix.
type Report struct {
	Total    int
	ByStatus map[Status]int
	ByRoute  map[Route]map[Status]int
	// Reasons lists, in sorted order, every fact that keeps Complete false.
	Reasons  []string
	Complete bool
}

var (
	ErrEmptyMatrix         = errors.New("routematrix: empty matrix")
	ErrEmptyClass          = errors.New("routematrix: empty sensitive class")
	ErrDuplicateClass      = errors.New("routematrix: duplicate sensitive class")
	ErrUnknownClass        = errors.New("routematrix: cell class not registered")
	ErrUnknownRoute        = errors.New("routematrix: unknown route")
	ErrUnknownTreatment    = errors.New("routematrix: unknown treatment")
	ErrTreatmentNotAllowed = errors.New("routematrix: treatment not allowed for route")
	ErrUnknownStatus       = errors.New("routematrix: unknown status")
	ErrUnknownEvidence     = errors.New("routematrix: unknown evidence kind")
	ErrEvidenceNotAllowed  = errors.New("routematrix: evidence kind not allowed for route")
	ErrDuplicateEvidence   = errors.New("routematrix: duplicate evidence kind in cell")
	ErrMissingCell         = errors.New("routematrix: missing cell")
	ErrDuplicateCell       = errors.New("routematrix: duplicate cell")
	ErrMissingTestRef      = errors.New("routematrix: evidence without test ref")
	ErrMalformedTestRef    = errors.New("routematrix: malformed test ref")
	ErrMissingReason       = errors.New("routematrix: pending cell without reason")
	ErrNilLookup           = errors.New("routematrix: nil ref lookup")
	ErrMissingRef          = errors.New("routematrix: test ref not found")
)

// allowedTreatments is the implementer's per-route treatment policy, OPEN (see
// doc.go, OPEN 2). The E06 table has no treatment column; this reads its
// filtered-materialization and proposal-restriction columns. Declared read and
// Bash view allow every treatment. Direct Pi/runtime has no proposal rule, so
// proposal-denied is rejected. The provider broker rule is OPEN: the broker is
// never treated as confidential, so only proposal-denied remains.
var allowedTreatments = map[Route]map[Treatment]bool{
	RouteDeclaredRead: {
		TreatmentOmitted: true, TreatmentMarker: true,
		TreatmentUnreachable: true, TreatmentProposalDenied: true,
	},
	RouteBashView: {
		TreatmentOmitted: true, TreatmentMarker: true,
		TreatmentUnreachable: true, TreatmentProposalDenied: true,
	},
	RouteDirectRuntime: {
		TreatmentOmitted: true, TreatmentMarker: true, TreatmentUnreachable: true,
	},
	RouteProviderBroker: {
		TreatmentProposalDenied: true,
	},
}

// requiredEvidence lists, per route, the evidence kinds a Covered cell must carry.
// A cell may carry no other kinds. The reading is OPEN (see doc.go, OPEN 5): the
// E06 required-test column names read attempts for the read routes and a
// provider-channel classification for the broker. The successful-read requirement
// is an interpretation of the "separate evidence types" sentence.
var requiredEvidence = map[Route][]EvidenceKind{
	RouteDeclaredRead:   {EvidenceAttemptedRead, EvidenceSuccessfulRead},
	RouteBashView:       {EvidenceAttemptedRead, EvidenceSuccessfulRead},
	RouteDirectRuntime:  {EvidenceAttemptedRead, EvidenceSuccessfulRead},
	RouteProviderBroker: {EvidenceProviderChannelDisclosure},
}

// testRefPattern accepts "package/path.TestName": at least one slash, segments
// of letters, digits, underscore, dot and hyphen, and a Go-style Test name that
// starts with an uppercase letter, digit or underscore after "Test".
var testRefPattern = regexp.MustCompile(
	`^([A-Za-z0-9_][A-Za-z0-9_.-]*/)+[A-Za-z0-9_][A-Za-z0-9_.-]*\.Test[A-Z0-9_][A-Za-z0-9_]*$`)

const digestDomain = "routematrix/v2"

// Validate checks a registered matrix. It rejects an empty matrix, duplicate or
// empty classes, unknown routes, classes, treatments, statuses or evidence kinds,
// treatments the policy does not allow for a route, evidence kinds the route does
// not require, duplicate evidence kinds in a cell, evidence without or with a
// malformed test reference, Pending_Guest cells without a reason, duplicate cells,
// and missing (route, class) cells. It does not check that a Covered cell carries
// every required evidence kind, that references resolve, or that references are
// unique. Coverage checks those.
func Validate(m Matrix) error {
	if len(m.Classes) == 0 || len(m.Cells) == 0 {
		return ErrEmptyMatrix
	}
	classes := make(map[SensitiveClass]bool, len(m.Classes))
	for _, c := range m.Classes {
		if c == "" {
			return ErrEmptyClass
		}
		if classes[c] {
			return fmt.Errorf("%w: %q", ErrDuplicateClass, c)
		}
		classes[c] = true
	}
	seen := make(map[cellKey]bool, len(m.Cells))
	for _, cell := range m.Cells {
		if err := validateCell(cell, classes); err != nil {
			return err
		}
		k := cellKey{cell.Route, cell.Class}
		if seen[k] {
			return fmt.Errorf("%w: route %q class %q", ErrDuplicateCell, cell.Route, cell.Class)
		}
		seen[k] = true
	}
	for _, r := range Routes {
		for _, c := range m.Classes {
			if !seen[cellKey{r, c}] {
				return fmt.Errorf("%w: route %q class %q", ErrMissingCell, r, c)
			}
		}
	}
	return nil
}

type cellKey struct {
	route Route
	class SensitiveClass
}

func validateCell(c Cell, classes map[SensitiveClass]bool) error {
	allowed, ok := allowedTreatments[c.Route]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRoute, c.Route)
	}
	if !classes[c.Class] {
		return fmt.Errorf("%w: %q", ErrUnknownClass, c.Class)
	}
	if !knownTreatment(c.Treatment) {
		return fmt.Errorf("%w: %q", ErrUnknownTreatment, c.Treatment)
	}
	if !allowed[c.Treatment] {
		return fmt.Errorf("%w: route %q treatment %q", ErrTreatmentNotAllowed, c.Route, c.Treatment)
	}
	switch c.Status {
	case StatusCovered:
	case StatusPendingGuest:
		if strings.TrimSpace(c.Reason) == "" {
			return fmt.Errorf("%w: route %q class %q", ErrMissingReason, c.Route, c.Class)
		}
	case StatusMissing:
	default:
		return fmt.Errorf("%w: %q", ErrUnknownStatus, c.Status)
	}
	return validateEvidence(c)
}

func validateEvidence(c Cell) error {
	applicable := make(map[EvidenceKind]bool, len(requiredEvidence[c.Route]))
	for _, k := range requiredEvidence[c.Route] {
		applicable[k] = true
	}
	seen := make(map[EvidenceKind]bool, len(c.Evidence))
	for _, e := range c.Evidence {
		if !knownEvidence(e.Kind) {
			return fmt.Errorf("%w: %q", ErrUnknownEvidence, e.Kind)
		}
		if !applicable[e.Kind] {
			return fmt.Errorf("%w: route %q kind %q", ErrEvidenceNotAllowed, c.Route, e.Kind)
		}
		if seen[e.Kind] {
			return fmt.Errorf("%w: route %q class %q kind %q", ErrDuplicateEvidence, c.Route, c.Class, e.Kind)
		}
		seen[e.Kind] = true
		if e.TestRef == "" {
			return fmt.Errorf("%w: route %q class %q kind %q", ErrMissingTestRef, c.Route, c.Class, e.Kind)
		}
		if !testRefPattern.MatchString(e.TestRef) {
			return fmt.Errorf("%w: %q", ErrMalformedTestRef, e.TestRef)
		}
	}
	return nil
}

func knownTreatment(t Treatment) bool {
	switch t {
	case TreatmentOmitted, TreatmentMarker, TreatmentUnreachable, TreatmentProposalDenied:
		return true
	}
	return false
}

func knownEvidence(k EvidenceKind) bool {
	switch k {
	case EvidenceAttemptedRead, EvidenceSuccessfulRead, EvidenceProviderChannelDisclosure:
		return true
	}
	return false
}

// Coverage validates the matrix, summarises it by status and by route, and sets
// Complete. The lookup resolves test references and is required: a nil lookup
// leaves every reference unverified. Complete is true only when all of these hold,
// and Reasons names each one that fails:
//   - the lookup is not nil;
//   - every cell is Covered;
//   - every Covered cell carries every evidence kind its route requires;
//   - every test reference resolves through the lookup;
//   - no test reference is reused, so each reference appears in exactly one
//     (route, class, kind) slot across the whole matrix. This uniqueness rule is
//     an implementer choice, not a spec quotation.
//
// An invalid matrix returns its Validate error and a zero Report.
func Coverage(m Matrix, lookup func(ref string) bool) (Report, error) {
	if err := Validate(m); err != nil {
		return Report{}, err
	}
	statuses := []Status{StatusCovered, StatusPendingGuest, StatusMissing}
	rep := Report{
		Total:    len(m.Cells),
		ByStatus: make(map[Status]int, len(statuses)),
		ByRoute:  make(map[Route]map[Status]int, len(Routes)),
	}
	for _, s := range statuses {
		rep.ByStatus[s] = 0
	}
	for _, r := range Routes {
		rep.ByRoute[r] = make(map[Status]int, len(statuses))
		for _, s := range statuses {
			rep.ByRoute[r][s] = 0
		}
	}
	for _, c := range m.Cells {
		rep.ByStatus[c.Status]++
		rep.ByRoute[c.Route][c.Status]++
		if c.Status != StatusCovered {
			rep.Reasons = append(rep.Reasons, fmt.Sprintf("route %q class %q: status %s", c.Route, c.Class, c.Status))
		}
	}
	if lookup == nil {
		rep.Reasons = append(rep.Reasons, "no test-ref lookup supplied: references unverified")
	} else if err := RefsExist(m, lookup); err != nil {
		rep.Reasons = append(rep.Reasons, err.Error())
	}
	rep.Reasons = append(rep.Reasons, missingEvidence(m)...)
	rep.Reasons = append(rep.Reasons, reusedRefs(m)...)
	sort.Strings(rep.Reasons)
	rep.Complete = len(rep.Reasons) == 0
	return rep, nil
}

// missingEvidence reports each Covered cell that lacks a kind its route requires.
func missingEvidence(m Matrix) []string {
	var out []string
	for _, c := range m.Cells {
		if c.Status != StatusCovered {
			continue
		}
		have := make(map[EvidenceKind]bool, len(c.Evidence))
		for _, e := range c.Evidence {
			have[e.Kind] = true
		}
		for _, k := range requiredEvidence[c.Route] {
			if !have[k] {
				out = append(out, fmt.Sprintf("route %q class %q: missing required evidence kind %s", c.Route, c.Class, k))
			}
		}
	}
	return out
}

// reusedRefs reports each test reference that occupies more than one evidence slot.
func reusedRefs(m Matrix) []string {
	slots := make(map[string][]string)
	for _, c := range m.Cells {
		for _, e := range c.Evidence {
			if e.TestRef == "" {
				continue
			}
			slots[e.TestRef] = append(slots[e.TestRef], fmt.Sprintf("%s/%s/%s", c.Route, c.Class, e.Kind))
		}
	}
	var out []string
	for ref, where := range slots {
		if len(where) < 2 {
			continue
		}
		sort.Strings(where)
		out = append(out, fmt.Sprintf("test ref %q reused in %d evidence slots: %s",
			ref, len(where), strings.Join(where, ", ")))
	}
	return out
}

// Digest returns the lowercase hex SHA-256 of a canonical encoding of the matrix.
// Classes are sorted. Each cell is encoded on its own with its evidence sorted by
// kind and reference, and the cell encodings are sorted, so the digest does not
// depend on input order. Every string is length-prefixed, so field boundaries
// cannot collide. The digest covers every Cell field, every evidence entry and the
// class list. It does not validate the matrix and never panics.
func Digest(m Matrix) string {
	classes := append([]SensitiveClass(nil), m.Classes...)
	sort.Slice(classes, func(i, j int) bool { return classes[i] < classes[j] })
	cells := make([][]byte, 0, len(m.Cells))
	for _, c := range m.Cells {
		cells = append(cells, encodeCell(c))
	}
	sort.Slice(cells, func(i, j int) bool { return bytes.Compare(cells[i], cells[j]) < 0 })

	h := sha256.New()
	writeString(h, digestDomain)
	writeCount(h, len(classes))
	for _, c := range classes {
		writeString(h, string(c))
	}
	writeCount(h, len(cells))
	for _, enc := range cells {
		writeCount(h, len(enc))
		h.Write(enc)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func encodeCell(c Cell) []byte {
	var buf bytes.Buffer
	writeString(&buf, string(c.Route))
	writeString(&buf, string(c.Class))
	writeString(&buf, string(c.Treatment))
	writeString(&buf, string(c.Status))
	writeString(&buf, c.Reason)
	ev := append([]Evidence(nil), c.Evidence...)
	sort.Slice(ev, func(i, j int) bool {
		if ev[i].Kind != ev[j].Kind {
			return ev[i].Kind < ev[j].Kind
		}
		return ev[i].TestRef < ev[j].TestRef
	})
	writeCount(&buf, len(ev))
	for _, e := range ev {
		writeString(&buf, string(e.Kind))
		writeString(&buf, e.TestRef)
	}
	return buf.Bytes()
}

func writeString(w io.Writer, s string) {
	writeCount(w, len(s))
	w.Write([]byte(s))
}

func writeCount(w io.Writer, n int) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(n))
	w.Write(buf[:])
}

// RefsExist reports every non-empty test reference in any evidence entry that
// lookup does not resolve. Each distinct missing reference is named once, in
// sorted order, and the error wraps ErrMissingRef. Empty references are not looked
// up. It does not validate the matrix; call Validate first. A nil lookup is an error.
func RefsExist(m Matrix, lookup func(ref string) bool) error {
	if lookup == nil {
		return ErrNilLookup
	}
	checked := make(map[string]bool)
	var missing []string
	for _, c := range m.Cells {
		for _, e := range c.Evidence {
			if e.TestRef == "" || checked[e.TestRef] {
				continue
			}
			checked[e.TestRef] = true
			if !lookup(e.TestRef) {
				missing = append(missing, e.TestRef)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%w: %s", ErrMissingRef, strings.Join(missing, ", "))
}
