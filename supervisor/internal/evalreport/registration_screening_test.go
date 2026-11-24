package evalreport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistrationScreeningRecordDigestIsOptionalAndBound(t *testing.T) {
	r := Registration{Domain: RegistrationDomain, FreezeCommit: strings.Repeat("a", 40), Reviewers: []string{"owner"}}
	without, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), "screening_record_digest") {
		t.Fatalf("a record without a screening record names the field, so older record digests would move: %s", without)
	}
	base, err := r.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	r.ScreeningRecordDigest = "sha256:" + strings.Repeat("b", 64)
	bound, err := r.ComputeDigest()
	if err != nil || bound == base {
		t.Fatalf("the screening record digest does not change the record digest: %s, %v", bound, err)
	}
}
