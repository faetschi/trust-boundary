package guestevidence

import "testing"

// run.json pins the runtime bundle and the runner (service binary) the runs of the guest evidence must share with
// the pilot; ParseRunBinding hands both to the producers and the validators.
func TestParseRunBindingReadsTheRuntimeBundleAndRunnerPins(t *testing.T) {
	doc := `{"freeze_commit":"` + testCommit + `","v_image_digest":"` + digestOf("image") + `","pins":{` +
		`"frozen_source_digest":"` + digestOf("source") + `","base_template_digest":"` + digestOf("base") + `",` +
		`"admin_pubkey_digest":"` + digestOf("key") + `","runtime_bundle_digest":"` + digestOf("bundle") + `","runner_digest":"` + digestOf("runner") + `"}}`
	b, err := ParseRunBinding([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if b.RuntimeBundleDigest != digestOf("bundle") || b.RunnerDigest != digestOf("runner") {
		t.Fatalf("runtime bundle %q, runner %q", b.RuntimeBundleDigest, b.RunnerDigest)
	}
}
