package routematrix

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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

// Treatment is how a route treats a sensitive class, as named by E06.
type Treatment string

const (
	TreatmentOmitted        Treatment = "omitted"
	TreatmentMarker         Treatment = "marker"
	TreatmentUnreachable    Treatment = "unreachable"
	TreatmentProposalDenied Treatment = "proposal-denied"
)

// Status is the evidence status of one cell.
type Status string

const (
	StatusCovered      Status = "Covered"
	StatusPendingGuest Status = "Pending_Guest"
	StatusMissing      Status = "Missing"
)

// Cell is one (route, class) entry of the registered matrix.
type Cell struct {
	Route     Route
	Class     SensitiveClass
	Treatment Treatment
	TestRef   string
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
	ErrMissingCell         = errors.New("routematrix: missing cell")
	ErrDuplicateCell       = errors.New("routematrix: duplicate cell")
	ErrMissingTestRef      = errors.New("routematrix: covered cell without test ref")
	ErrMalformedTestRef    = errors.New("routematrix: malformed test ref")
	ErrMissingReason       = errors.New("routematrix: pending cell without reason")
	ErrNilLookup           = errors.New("routematrix: nil ref lookup")
	ErrMissingRef          = errors.New("routematrix: test ref not found")
)

// allowedTreatments is the spec's E06 treatment policy per route. Declared read
// and Bash view allow every treatment. Direct Pi/runtime has no proposal rule.
// The provider broker is never treated as confidential, so only the
// non-confidential proposal-denied treatment remains (see doc.go, OPEN 2).
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

// testRefPattern accepts "package/path.TestName": at least one slash, segments
// of letters, digits, underscore, dot and hyphen, and a Go-style Test name that
// starts with an uppercase letter, digit or underscore after "Test".
var testRefPattern = regexp.MustCompile(
	`^([A-Za-z0-9_][A-Za-z0-9_.-]*/)+[A-Za-z0-9_][A-Za-z0-9_.-]*\.Test[A-Z0-9_][A-Za-z0-9_]*$`)

const digestDomain = "routematrix/v1"

type cellKey struct {
	route Route
	class SensitiveClass
}

// Validate checks a registered matrix. It rejects an empty matrix, duplicate or
// empty classes, unknown routes, classes, treatments or statuses, treatments the
// spec does not allow for a route, malformed test references, Covered cells
// without a test reference, Pending_Guest cells without a reason, duplicate
// cells, and missing (route, class) cells. It does not check that a test
// reference resolves; see RefsExist.
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
		if c.TestRef == "" {
			return fmt.Errorf("%w: route %q class %q", ErrMissingTestRef, c.Route, c.Class)
		}
	case StatusPendingGuest:
		if strings.TrimSpace(c.Reason) == "" {
			return fmt.Errorf("%w: route %q class %q", ErrMissingReason, c.Route, c.Class)
		}
	case StatusMissing:
	default:
		return fmt.Errorf("%w: %q", ErrUnknownStatus, c.Status)
	}
	if c.TestRef != "" && !testRefPattern.MatchString(c.TestRef) {
		return fmt.Errorf("%w: %q", ErrMalformedTestRef, c.TestRef)
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

// Coverage validates the matrix and summarises it by status and by route.
// Complete is true only when every cell is Covered. An invalid matrix returns
// its Validate error and a zero Report, so Complete is false.
func Coverage(m Matrix) (Report, error) {
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
	}
	rep.Complete = rep.ByStatus[StatusCovered] == rep.Total
	return rep, nil
}

// Digest returns the lowercase hex SHA-256 of a canonical encoding of the
// matrix. Classes and cells are sorted, so the digest does not depend on input
// order. Every string is length-prefixed, so field boundaries cannot collide.
// The digest covers every Cell field and the class list. It does not validate
// the matrix and never panics.
func Digest(m Matrix) string {
	classes := append([]SensitiveClass(nil), m.Classes...)
	sort.Slice(classes, func(i, j int) bool { return classes[i] < classes[j] })
	cells := append([]Cell(nil), m.Cells...)
	sort.Slice(cells, func(i, j int) bool { return cellLess(cells[i], cells[j]) })

	h := sha256.New()
	writeString(h, digestDomain)
	writeCount(h, len(classes))
	for _, c := range classes {
		writeString(h, string(c))
	}
	writeCount(h, len(cells))
	for _, c := range cells {
		writeString(h, string(c.Route))
		writeString(h, string(c.Class))
		writeString(h, string(c.Treatment))
		writeString(h, string(c.Status))
		writeString(h, c.TestRef)
		writeString(h, c.Reason)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func cellLess(a, b Cell) bool {
	fields := [][2]string{
		{string(a.Route), string(b.Route)},
		{string(a.Class), string(b.Class)},
		{string(a.Treatment), string(b.Treatment)},
		{string(a.Status), string(b.Status)},
		{a.TestRef, b.TestRef},
		{a.Reason, b.Reason},
	}
	for _, f := range fields {
		if f[0] != f[1] {
			return f[0] < f[1]
		}
	}
	return false
}

func writeString(h hash.Hash, s string) {
	writeCount(h, len(s))
	h.Write([]byte(s))
}

func writeCount(h hash.Hash, n int) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(n))
	h.Write(buf[:])
}

// RefsExist reports every non-empty TestRef in the matrix that lookup does not
// resolve. Each distinct missing reference is named once, in sorted order, and
// the error wraps ErrMissingRef. Empty references are not looked up. It does
// not validate the matrix; call Validate first. A nil lookup is an error.
func RefsExist(m Matrix, lookup func(ref string) bool) error {
	if lookup == nil {
		return ErrNilLookup
	}
	checked := make(map[string]bool)
	var missing []string
	for _, c := range m.Cells {
		if c.TestRef == "" || checked[c.TestRef] {
			continue
		}
		checked[c.TestRef] = true
		if !lookup(c.TestRef) {
			missing = append(missing, c.TestRef)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%w: %s", ErrMissingRef, strings.Join(missing, ", "))
}
