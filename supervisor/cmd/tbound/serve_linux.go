//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"io"
)

// runServe keeps the production route fail-closed. The explicit fixture route
// is implemented separately so a future host agent can provide a reviewed
// profile without changing the CLI's refusal semantics.
func runServe(args []string, transcript io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pi := flags.Bool("pi", false, "run the native Pi harness")
	nativeFixture := flags.Bool("native-fixture", false, "run the explicit non-claim-bearing native lifecycle fixture")
	devFixture := flags.Bool("dev-fixture", false, "launch the actual pinned Pi SDK worker through the offline non-claim-bearing development composition")
	nativeHostProfile := flags.String("native-host-profile", "", "trusted host profile receipt (production only)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !*pi {
		return errors.New("tbound serve requires --pi")
	}
	if *devFixture && *nativeFixture {
		return errors.New("--dev-fixture and --native-fixture are mutually exclusive")
	}
	if *devFixture && *nativeHostProfile != "" {
		return errors.New("--dev-fixture and --native-host-profile are mutually exclusive")
	}
	if *devFixture {
		return runDevelopmentPiFixture(context.Background(), transcript)
	}
	if *nativeFixture {
		return runNativeFixture(context.Background(), transcript)
	}
	if *nativeHostProfile == "" {
		return errors.New("native Pi host profile is missing; production --pi launch is refused")
	}
	return errors.New("native Pi host profile is not an installed, attested production runtime")
}
