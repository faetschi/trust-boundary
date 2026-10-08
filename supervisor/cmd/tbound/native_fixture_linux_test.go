//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"tbound/supervisor/internal/publication"
)

func TestRunNativeFixtureExercisesRealDurableContinuationAndPublication(t *testing.T) {
	usePrivateFixtureTemp(t)
	var output bytes.Buffer
	if err := runNativeFixture(context.Background(), &output); err != nil {
		t.Fatalf("real native fixture failed: %v", err)
	}
	var receipt struct {
		Mode             string `json:"mode"`
		ClaimBearing     bool   `json:"claim_bearing"`
		ProviderExchange bool   `json:"provider_exchange"`
		Containment      string `json:"containment"`
		Publication      string `json:"publication_status"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &receipt); err != nil {
		t.Fatalf("decode real native fixture receipt: %v; output=%q", err, output.String())
	}
	if receipt.Mode != "native-fixture" || receipt.ClaimBearing || receipt.ProviderExchange ||
		receipt.Containment != "not-established" || receipt.Publication != string(publication.StatusSucceeded) {
		t.Fatalf("real native fixture receipt lost claim boundary or publication result: %+v", receipt)
	}
}

func TestNativeFixtureReceiptRejectsFailedPublication(t *testing.T) {
	if err := writeNativeFixtureReceipt(io.Discard, publication.StatusFailed); err == nil {
		t.Fatal("FAILED publication was reported as a successful fixture receipt")
	}
}

func TestNativeFixtureTerminalFailedPublicationIsAnError(t *testing.T) {
	if err := requireNativeFixturePublicationSuccess(publication.Result{Status: publication.StatusFailed}); err == nil {
		t.Fatal("terminal FAILED publication was accepted")
	}
}

func TestNativeFixtureRunPropagatesFailingReceiptWriter(t *testing.T) {
	usePrivateFixtureTemp(t)
	want := errors.New("receipt sink failed")
	err := run([]string{"serve", "--pi", "--native-fixture"}, failingFixtureWriter{err: want}, io.Discard)
	if !errors.Is(err, want) {
		t.Fatalf("native fixture CLI writer error = %v, want %v", err, want)
	}
}

func usePrivateFixtureTemp(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	privateTemp, err := os.MkdirTemp(home, "tbound-native-test-tmp-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privateTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(privateTemp) })
	t.Setenv("TMPDIR", privateTemp)
}

func TestNativeFixtureReceiptPropagatesWriterError(t *testing.T) {
	want := errors.New("receipt sink failed")
	err := writeNativeFixtureReceipt(failingFixtureWriter{err: want}, publication.StatusSucceeded)
	if !errors.Is(err, want) {
		t.Fatalf("receipt writer error = %v, want %v", err, want)
	}
}

type failingFixtureWriter struct{ err error }

func (w failingFixtureWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("write fixture receipt: %w", w.err)
}
