package corpusadmit

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"tbound/supervisor/internal/evalcorpus"
)

type sourcePin struct {
	Revision     string   `json:"rev"`
	License      string   `json:"license"`
	Digests      []string `json:"digests"`
	LicenseFiles []string `json:"license_files"`
}

func checkSource(c evalcorpus.Case, set fs.FS) ConstructCheck {
	r := ConstructCheck{Name: "source-license", Status: "fail"}
	bad := func(reason string) ConstructCheck { r.Reason = reason; return r }
	mr := c.MeaningRecord
	if mr == nil {
		return bad("missing D37 meaning record")
	}
	raw, err := fs.ReadFile(set, "sources.json")
	if err != nil {
		return bad(err.Error())
	}
	var sources struct {
		Pins map[string]sourcePin `json:"pins"`
	}
	if err := json.Unmarshal(raw, &sources); err != nil {
		return bad("sources.json: " + err.Error())
	}
	pin, ok := sources.Pins[mr.BenchmarkKey]
	if !ok || pin.Revision == "" || pin.Revision != mr.BenchmarkRevision {
		return bad("benchmark key/revision is not pinned in sources.json")
	}
	if !slices.Contains(pin.Digests, mr.BenchmarkDigest) {
		return bad("benchmark digest is not pinned to this benchmark key in sources.json")
	}
	if _, ok := artifactName("", mr.BenchmarkDigest); !ok {
		return bad("benchmark digest is not a sha256 digest")
	}
	if len(pin.LicenseFiles) == 0 || pin.License == "" || mr.License == "" {
		return bad("source pin needs license and license_files")
	}
	notices, err := fs.ReadFile(set, "THIRD_PARTY_NOTICES.md")
	if err != nil {
		return bad(err.Error())
	}
	if !strings.Contains(string(notices), pin.Revision) {
		return bad("pinned revision is absent from THIRD_PARTY_NOTICES.md")
	}
	for _, file := range pin.LicenseFiles {
		if !fs.ValidPath(file) || !strings.HasPrefix(file, "licenses/") {
			return bad("license path must stay under licenses/")
		}
		info, err := fs.Stat(set, file)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return bad("missing, empty or non-regular license file: " + file)
		}
		if !strings.Contains(string(notices), "`"+file+"`") {
			return bad("license file is not listed in THIRD_PARTY_NOTICES.md: " + file)
		}
		text, err := fs.ReadFile(set, file)
		if err != nil {
			return bad(err.Error())
		}
		lines := strings.Split(string(text), "\n")
		for i, line := range lines {
			if restrictedLicense(line) {
				condition := strings.TrimSpace(line)
				for j := i + 1; j < len(lines) && strings.TrimSpace(lines[j]) != ""; j++ {
					condition += " " + strings.TrimSpace(lines[j])
				}
				r.Conditions = append(r.Conditions, file+": "+condition)
			}
		}
	}
	if restrictedLicense(pin.License) || restrictedLicense(mr.License) {
		r.Conditions = append(r.Conditions, pin.License+"; "+mr.License)
	}
	r.Status, r.Reason = "pass", "benchmark key and revision verified, digest listed in the sources.json pin, retained license files verified; license use still needs advisor review"
	if len(r.Conditions) != 0 {
		r.Reason += "; restricted license conditions flagged"
	}
	return r
}

func restrictedLicense(s string) bool {
	s = strings.ToLower(s)
	for _, term := range []string{"non-commercial", "noncommercial", "-nc", "safety", "acceptable use", "use restrictions", "machine learning restriction", "may not be used as", "prohibited use", "harmful", "responsible ai", "research only"} {
		if strings.Contains(s, term) {
			return true
		}
	}
	return false
}

func checkRoots(c evalcorpus.Case, cases []evalcorpus.Case) ConstructCheck {
	r := ConstructCheck{Name: "root-unique", Status: "pass", Reason: "meaning-record root occurs once in this set"}
	if c.MeaningRecord == nil || strings.TrimSpace(c.MeaningRecord.RootID) == "" {
		r.Status, r.Reason = "fail", "missing source root id"
		return r
	}
	var ids []string
	for _, other := range cases {
		if other.MeaningRecord != nil && other.MeaningRecord.RootID == c.MeaningRecord.RootID {
			ids = append(ids, other.CaseID)
		}
	}
	if len(ids) != 1 {
		r.Status, r.Reason = "fail", fmt.Sprintf("root id occurs %d times: %s", len(ids), strings.Join(ids, ", "))
	}
	return r
}

func checkOverlap(c evalcorpus.Case, other *evalcorpus.Manifest) ConstructCheck {
	r := ConstructCheck{Name: "root-disjoint", Status: "not-checked", Reason: "second manifest not supplied"}
	if other == nil {
		return r
	}
	if other.SchemaVersion != evalcorpus.SchemaVersion || len(other.Cases) == 0 {
		r.Status, r.Reason = "fail", "second manifest has no valid schema/case roots"
		return r
	}
	if c.MeaningRecord == nil || c.MeaningRecord.RootID == "" {
		r.Status, r.Reason = "fail", "missing source root id"
		return r
	}
	for _, peer := range other.Cases {
		if peer.MeaningRecord == nil || peer.MeaningRecord.RootID == "" {
			r.Status, r.Reason = "fail", "second manifest has a missing source root id"
			return r
		}
		if peer.MeaningRecord.RootID == c.MeaningRecord.RootID {
			r.Status, r.Reason = "fail", "root overlaps second manifest case "+peer.CaseID
			return r
		}
	}
	r.Status, r.Reason = "pass", "root is disjoint from second manifest"
	return r
}
