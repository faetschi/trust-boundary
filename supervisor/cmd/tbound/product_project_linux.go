//go:build linux

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tbound/supervisor/internal/projectbundle"
)

const projectUsage = `usage: tbound project <command> [flags]

  bundle  --source DIR [--exclude PATH]... --out FILE
          Scan the original project and write a tbound-project-bundle/v1 file. Run it as the user
          who owns the project. .git is always excluded; --exclude paths are relative to --source.
  import  --bundle FILE --dest DIR [--manifest-out FILE]
          Validate a bundle and create the new project directory DEST (it must not exist and its
          parent must be private). Run it as the service. --manifest-out saves the baseline manifest.
  export  --baseline-manifest FILE --current DIR --out FILE
          Compare a managed project with its baseline manifest and write a tbound-project-export/v1
          file. Run it as the service.
  apply   --bundle FILE --worktree DIR --state-dir DIR [--exclude PATH]...
          Verify an export and write --state-dir/tbound-export.patch without changing the worktree.
          Run it as the user, with the same --exclude paths as bundle. State directory must be 0700.
          --direct --writer-exclusion-held writes directly. You must guarantee no concurrent writer;
          a failure retains a recovery record when changes occurred or a target parent moved.
`

type pathList []string

func (p *pathList) String() string { return strings.Join(*p, ",") }

func (p *pathList) Set(value string) error {
	*p = append(*p, value)
	return nil
}

func runProject(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, projectUsage)
		return errors.New("project: a command is required")
	}
	switch args[0] {
	case "bundle":
		return runProjectBundle(args[1:], stdout, stderr)
	case "import":
		return runProjectImport(args[1:], stdout, stderr)
	case "export":
		return runProjectExport(args[1:], stdout, stderr)
	case "apply":
		return runProjectApply(args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, projectUsage)
	return fmt.Errorf("project: unknown command %q", args[0])
}

// parseProjectFlags parses a strict flag set and requires every named flag to be set.
func parseProjectFlags(name string, fs *flag.FlagSet, args []string, stderr io.Writer, required ...string) error {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("project %s: unexpected arguments %q", name, fs.Args())
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		if f.Value.String() != "" {
			set[f.Name] = true
		}
	})
	for _, r := range required {
		if !set[r] {
			return fmt.Errorf("project %s: --%s is required", name, r)
		}
	}
	return nil
}

// refuseInside fails when file lies in dir, which would make the tool's own output part of its input.
func refuseInside(dirFlag, dir, fileFlag, file string) error {
	resolve := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		if parent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
			return filepath.Join(parent, filepath.Base(abs))
		}
		return abs
	}
	rel, err := filepath.Rel(resolve(dir), resolve(file))
	if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return fmt.Errorf("--%s must not be inside --%s", fileFlag, dirFlag)
	}
	return nil
}

func runProjectBundle(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("project bundle", flag.ContinueOnError)
	source := fs.String("source", "", "original project directory")
	out := fs.String("out", "", "bundle file to create")
	var exclude pathList
	fs.Var(&exclude, "exclude", "relative path to leave out (repeatable)")
	if err := parseProjectFlags("bundle", fs, args, stderr, "source", "out"); err != nil {
		return err
	}
	if err := refuseInside("source", *source, "out", *out); err != nil {
		return err
	}
	var built projectbundle.BuildResult
	err := projectbundle.WriteNewFile(*out, func(w io.Writer) error {
		var err error
		built, err = projectbundle.Build(*source, projectbundle.BuildOptions{Exclude: exclude, Limits: projectbundle.DefaultLimits()}, w)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "included: %s\nexcluded: %d\n", summarize(built.Manifest), len(built.Excluded))
	for _, p := range built.Excluded {
		fmt.Fprintf(stdout, "  %s\n", p)
	}
	fmt.Fprintf(stdout, "manifest digest: %s\nwrote %s\n", built.Digest, *out)
	return nil
}

func summarize(m projectbundle.Manifest) string {
	files, dirs, bytes := 0, 0, int64(0)
	for _, e := range m.Entries {
		if e.Type == projectbundle.TypeFile {
			files++
			bytes += e.Size
		} else {
			dirs++
		}
	}
	return fmt.Sprintf("%d entries (%d files, %d directories, %d bytes)", len(m.Entries), files, dirs, bytes)
}

func runProjectImport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("project import", flag.ContinueOnError)
	bundle := fs.String("bundle", "", "bundle file")
	dest := fs.String("dest", "", "new project directory")
	manifestOut := fs.String("manifest-out", "", "file to create with the baseline manifest")
	if err := parseProjectFlags("import", fs, args, stderr, "bundle", "dest"); err != nil {
		return err
	}
	if err := refuseInside("dest", *dest, "manifest-out", *manifestOut); *manifestOut != "" && err != nil {
		return err
	}
	f, err := os.Open(*bundle)
	if err != nil {
		return err
	}
	defer f.Close()
	manifest, digest, err := projectbundle.Import(bufio.NewReaderSize(f, 1<<20), *dest, projectbundle.DefaultLimits())
	if err != nil {
		return err
	}
	if *manifestOut != "" {
		data, err := manifest.MarshalCanonical()
		if err == nil {
			err = projectbundle.WriteNewFile(*manifestOut, func(w io.Writer) error { _, err := w.Write(data); return err })
		}
		if err != nil {
			return fmt.Errorf("project %s was imported, but the manifest could not be written: %w", *dest, err)
		}
	}
	fmt.Fprintf(stdout, "imported %s into %s\nmanifest digest: %s\n", summarize(manifest), *dest, digest)
	return nil
}

func runProjectExport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("project export", flag.ContinueOnError)
	baseline := fs.String("baseline-manifest", "", "baseline manifest file written by import")
	current := fs.String("current", "", "current managed project directory")
	out := fs.String("out", "", "export file to create")
	if err := parseProjectFlags("export", fs, args, stderr, "baseline-manifest", "current", "out"); err != nil {
		return err
	}
	if err := refuseInside("current", *current, "out", *out); err != nil {
		return err
	}
	f, err := os.Open(*baseline)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, projectbundle.MaxHeaderBytes+1))
	f.Close()
	if err != nil {
		return err
	}
	limits := projectbundle.DefaultLimits()
	manifest, err := projectbundle.ParseManifest(data, limits)
	if err != nil {
		return fmt.Errorf("baseline manifest: %w", err)
	}
	var header projectbundle.ExportHeader
	var digest string
	err = projectbundle.WriteNewFile(*out, func(w io.Writer) error {
		var err error
		header, digest, err = projectbundle.BuildExport(manifest, *current, limits, w)
		return err
	})
	if err != nil {
		return err
	}
	counts := map[projectbundle.ChangeOp]int{}
	for _, c := range header.Changes {
		counts[c.Op]++
	}
	fmt.Fprintf(stdout, "changes: %d (create %d, modify %d, delete %d, chmod %d)\nbaseline digest: %s\ntarget digest: %s\nexport digest: %s\nwrote %s\n",
		len(header.Changes), counts[projectbundle.OpCreate], counts[projectbundle.OpModify], counts[projectbundle.OpDelete], counts[projectbundle.OpChmod],
		header.BaselineDigest, header.TargetDigest, digest, *out)
	return nil
}

func runProjectApply(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("project apply", flag.ContinueOnError)
	bundle := fs.String("bundle", "", "export file")
	worktree := fs.String("worktree", "", "clean Git worktree")
	stateDir := fs.String("state-dir", "", "existing caller-owned 0700 directory outside the worktree for the patch or recovery record")
	direct := fs.Bool("direct", false, "write the worktree directly; requires --writer-exclusion-held")
	exclusion := fs.Bool("writer-exclusion-held", false, "guarantee no concurrent writer can move or alter the worktree throughout direct apply")
	var exclude pathList
	fs.Var(&exclude, "exclude", "relative path left out of the baseline (repeatable)")
	if err := parseProjectFlags("apply", fs, args, stderr, "bundle", "worktree", "state-dir"); err != nil {
		return err
	}
	if *direct != *exclusion {
		return errors.New("project apply: direct writes require both --direct and --writer-exclusion-held")
	}
	mode := projectbundle.PatchMode
	if *direct {
		mode = projectbundle.DirectMode
	}
	f, err := os.Open(*bundle)
	if err != nil {
		return err
	}
	defer f.Close()
	changed, err := projectbundle.ApplyExport(f, *worktree, projectbundle.ApplyOptions{Mode: mode, WriterExclusionHeld: *exclusion, StateDir: *stateDir, Exclude: exclude, Limits: projectbundle.DefaultLimits()})
	if err != nil {
		return err
	}
	if !*direct {
		absWorktree, err := filepath.Abs(*worktree)
		if err != nil {
			return err
		}
		patch, err := filepath.Abs(filepath.Join(*stateDir, projectbundle.PatchFileName))
		if err != nil {
			return err
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
		fmt.Fprintf(stdout, "verified %d changes; worktree unchanged\nwrote %s\nApply as the worktree owner after excluding concurrent writers:\n  git -C %s apply --check -- %s\n  git -C %s apply -- %s\n", len(changed), patch, quote(absWorktree), quote(patch), quote(absWorktree), quote(patch))
		return nil
	}
	fmt.Fprintf(stdout, "applied %d changes to %s and verified the result\n", len(changed), *worktree)
	for _, p := range changed {
		fmt.Fprintf(stdout, "  %s\n", p)
	}
	return nil
}
