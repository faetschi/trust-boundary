package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/install"
)

func runDoctor(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "emit the preflight report as JSON")
	profileDir := flags.String("profile-dir", "/etc/tbound", "host profile directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	report := install.Preflight(context.Background(), install.DefaultOptions())

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			install.Report
			Profile *install.HostProfileVerification `json:"host_profile,omitempty"`
		}{Report: report})
	} else {
		fmt.Fprintln(stdout, report.Summary())
	}

	// Verify the admitted host profile when present. Absence is reported but
	// doctor still runs so the user sees every gap at once.
	opts := install.DefaultHostProfileOptions()
	opts.Dir = *profileDir
	if _, err := os.Stat(filepath.Join(*profileDir, install.ProfileJSONName)); err == nil {
		verification, verr := install.VerifyHostProfile(opts)
		if verr != nil {
			fmt.Fprintf(stdout, "\nhost profile: INVALID: %v\n", verr)
			return install.ErrNotReady{Failures: []string{"host-profile"}}
		}
		if !*asJSON {
			fmt.Fprintf(stdout, "\nhost profile: valid (%s)\n", verification.ProfileDigest)
		}
	} else if !*asJSON {
		fmt.Fprintf(stdout, "\nhost profile: not provisioned (operator step)\n")
	}

	if !report.Ready {
		return report.RequireReady()
	}
	return nil
}

func runInstall(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "/opt/tbound", "managed runtime root")
	etcDir := flags.String("etc", "/etc/tbound", "configuration directory")
	manifestPath := flags.String("manifest", "", "pinned runtime bundle manifest (required to install)")
	user := flags.String("user", "", "trial user for the systemd user unit (defaults to current user)")
	dryRun := flags.Bool("dry-run", false, "print the plan without changing the host")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	report := install.Preflight(context.Background(), install.DefaultOptions())
	if !*dryRun {
		fmt.Fprintln(stdout, report.Summary())
		if err := report.RequireReady(); err != nil {
			return err
		}
	}

	// The admitted host profile must verify before any managed file is written.
	profileOpts := install.DefaultHostProfileOptions()
	profileOpts.Dir = *etcDir
	verification, err := install.VerifyHostProfile(profileOpts)
	if err != nil && !*dryRun {
		return fmt.Errorf("host profile not admitted: %w", err)
	}

	targetUser := *user
	if targetUser == "" {
		targetUser = os.Getenv("USER")
		if targetUser == "" {
			targetUser = os.Getenv("USERNAME")
		}
	}
	plan, err := install.BuildIsolationPlan(targetUser, install.IsolationOptions{})
	if err != nil {
		return err
	}
	if verification.Valid {
		fmt.Fprintf(stdout, "host profile digest: %s\n", verification.ProfileDigest)
	}
	fmt.Fprintf(stdout, "isolation unit: %s\n", plan.UnitPath)

	if *dryRun {
		fmt.Fprintln(stdout, "dry-run: no changes made")
		return nil
	}
	if *manifestPath == "" {
		return errors.New("--manifest is required to install the pinned runtime bundle")
	}
	manifest, err := install.LoadBundleManifest(*manifestPath)
	if err != nil {
		return err
	}
	if err := install.ApplyBundle(context.Background(), *root, manifest, install.HTTPFetcher{}); err != nil {
		return fmt.Errorf("install runtime bundle: %w", err)
	}
	if err := install.MarkManaged(install.OSFS(), *root); err != nil {
		return fmt.Errorf("mark managed root: %w", err)
	}
	if err := install.MarkManaged(install.OSFS(), *etcDir); err != nil {
		return fmt.Errorf("mark managed config dir: %w", err)
	}
	brokerPath := filepath.Join(*etcDir, install.BrokerConfigFileName)
	if err := install.WriteBrokerConfig(brokerPath, []byte(install.BrokerConfigTemplate())); err != nil {
		return fmt.Errorf("write broker config: %w", err)
	}
	fmt.Fprintf(stdout, "installed runtime bundle to %s\n", *root)
	fmt.Fprintf(stdout, "wrote broker template %s (add the credential out-of-band)\n", brokerPath)
	fmt.Fprintln(stdout, "next: install the user unit, run `systemctl --user daemon-reload`, then `tbound doctor`")
	return nil
}

func runUninstall(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound uninstall", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "/opt/tbound", "managed runtime root")
	etcDir := flags.String("etc", "/etc/tbound", "configuration directory")
	unitPath := flags.String("unit", "", "user unit path to remove (optional)")
	dryRun := flags.Bool("dry-run", false, "print the plan without removing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	fs := install.OSFS()
	plan, err := install.PlanUninstall(fs, *root, *etcDir, *unitPath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(plan)
	if *dryRun {
		fmt.Fprintln(stdout, "dry-run: nothing removed")
		return nil
	}
	return install.ApplyUninstall(fs, plan)
}
