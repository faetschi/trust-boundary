// tbound-hint-eval is an offline comparison of recorded decision-model choices.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"tbound/supervisor/internal/providerbridge"
)

func main() {
	fixture := flag.String("fixtures", "internal/providerbridge/testdata/hint_eval.json", "frozen scenarios with fake or recorded answers; never calls a provider")
	manifestPath := flag.String("manifest", "", "optional finalized C-DH manifest; requires exactly its two model recordings")
	flag.Parse()
	if err := run(*fixture, *manifestPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path, manifestPath string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fixture providerbridge.HintEvalFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		return err
	}
	threshold, timeout := .8, time.Second
	manifestDigest := ""
	if manifestPath != "" {
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		var manifest providerbridge.CDHManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return err
		}
		manifestDigest, err = manifest.Digest()
		if err != nil {
			return err
		}
		for _, scenario := range fixture.Scenarios {
			if len(scenario.Recordings) != 2 {
				return fmt.Errorf("C-DH requires exactly two chooser recordings per scenario")
			}
			for _, model := range manifest.Models {
				if _, ok := scenario.Recordings[model]; !ok {
					return fmt.Errorf("missing registered model %s", model)
				}
			}
		}
		threshold, timeout = manifest.MinConfidence, time.Duration(manifest.TimeoutMS)*time.Millisecond
	}
	reports, err := providerbridge.EvaluateHintFixture(fixture, threshold, timeout)
	if err != nil {
		return err
	}
	output := struct {
		Note           string                          `json:"note"`
		ManifestDigest string                          `json:"manifest_digest,omitempty"`
		Reports        []providerbridge.HintEvalReport `json:"reports"`
	}{fixture.Note, manifestDigest, reports}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}
