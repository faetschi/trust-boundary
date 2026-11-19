//go:build linux

package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runend"
	"tbound/supervisor/internal/workflowrecovery"
)

// runEndSentinelDirs are the packages whose exported sentinels can reach a governed run's error: the service and
// session (this package), the provider bridge, broker and capture, the workflow ledger and the Pi runtime.
var runEndSentinelDirs = []string{".", "../../internal/providerbridge", "../../internal/broker",
	"../../internal/broker/openrouter", "../../internal/broker/protocol", "../../internal/workflowrecovery",
	"../../internal/piruntime"}

// runEndBoundDirs are the provider path: every bound there that a failure message names is a registry row.
var runEndBoundDirs = []string{"../../internal/providerbridge", "../../internal/broker", "../../internal/broker/openrouter"}

var runEndBoundWords = regexp.MustCompile(`(?i)exhausted|exceeds|budget|limit|too many|too large|ceiling|allowance|oversized|out of bounds|\bcap\b`)

func parseRunEndDir(t *testing.T, dir string) (*token.FileSet, []*ast.File) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no Go files in %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return fset, files
}

// runEndRows reads the registry rows from its source: the err selector (pkg.ErrX, or ErrX of this package), the
// bound message, whether the row is a carrier, and its class.
type runEndRow struct {
	err, bound, class string
	carrier           bool
}

func runEndRows(t *testing.T) []runEndRow {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run_end_registry_linux.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var rows []runEndRow
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "runEndRegistry" {
			return true
		}
		for _, element := range spec.Values[0].(*ast.CompositeLit).Elts {
			var row runEndRow
			for _, field := range element.(*ast.CompositeLit).Elts {
				kv := field.(*ast.KeyValueExpr)
				switch key := kv.Key.(*ast.Ident).Name; key {
				case "err":
					row.err = exprText(kv.Value)
				case "bound":
					row.bound, _ = strconv.Unquote(kv.Value.(*ast.BasicLit).Value)
				case "class":
					row.class = exprText(kv.Value)
				case "carrier":
					row.carrier = exprText(kv.Value) == "true"
				}
			}
			rows = append(rows, row)
		}
		return false
	})
	if len(rows) == 0 {
		t.Fatal("no runEndRegistry rows")
	}
	return rows
}

func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	}
	return fmt.Sprintf("%T", e)
}

// The registry classes every source that can end a governed step UNKNOWN. The test walks the sources and fails on
// one without a row: every exported sentinel of the run's packages, every bound a failure message on the provider
// path names, and every code of an UNKNOWN answer. An agent_ceiling bound must carry runend.ErrCeiling at its
// site, and no other bound may, so the classifier sees exactly the registered ceilings.
func TestRunEndRegistryClassesEverySource(t *testing.T) {
	rows := runEndRows(t)
	sentinelRows, boundRows := map[string]bool{}, map[string]runEndRow{}
	for _, row := range rows {
		if row.err != "" {
			sentinelRows[row.err] = true
		}
		if row.bound != "" {
			boundRows[row.bound] = row
		}
		if !strings.HasPrefix(row.class, "runend.") {
			t.Errorf("row %+v has no class", row)
		}
	}

	for _, dir := range runEndSentinelDirs {
		_, files := parseRunEndDir(t, dir)
		for _, file := range files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.VAR {
					continue
				}
				for _, spec := range gen.Specs {
					for _, name := range spec.(*ast.ValueSpec).Names {
						if !strings.HasPrefix(name.Name, "Err") {
							continue
						}
						qualified := file.Name.Name + "." + name.Name
						if file.Name.Name == "main" {
							qualified = name.Name
						}
						if !sentinelRows[qualified] {
							t.Errorf("sentinel %s has no run-end class", qualified)
						}
					}
				}
			}
		}
	}

	seenBounds := map[string]bool{}
	for _, dir := range runEndBoundDirs {
		fset, files := parseRunEndDir(t, dir)
		for _, file := range files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || len(call.Args) == 0 {
						return true
					}
					fun := exprText(call.Fun)
					if fun != "errors.New" && fun != "fmt.Errorf" {
						return true
					}
					lit, ok := call.Args[0].(*ast.BasicLit)
					if !ok {
						return true
					}
					message, _ := strconv.Unquote(lit.Value)
					if !runEndBoundWords.MatchString(message) {
						return true
					}
					seenBounds[message] = true
					row, registered := boundRows[message]
					if !registered {
						t.Errorf("%s: bound %q has no run-end class", fset.Position(call.Pos()), message)
						return true
					}
					marked := false
					for _, arg := range call.Args[1:] {
						marked = marked || exprText(arg) == "runend.ErrCeiling"
					}
					if ceiling := row.class == "runend.AgentCeiling" && !row.carrier; ceiling != marked {
						t.Errorf("%s: bound %q is %s but carries runend.ErrCeiling=%t", fset.Position(call.Pos()), message, row.class, marked)
					}
					return true
				})
			}
		}
	}
	for message := range boundRows {
		if !seenBounds[message] {
			t.Errorf("bound row %q names no failure message on the provider path", message)
		}
	}

	// Every code of an UNKNOWN answer: a literal in a piServiceResponse whose status is "unknown", or a code the
	// classifier chooses (runEndAnswer, which returns only runend codes).
	_, files := parseRunEndDir(t, ".")
	unknownCodes := 0
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || exprText(lit.Type) != "piServiceResponse" {
				return true
			}
			status, code := "", ""
			for _, element := range lit.Elts {
				kv, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				value, isLiteral := kv.Value.(*ast.BasicLit)
				switch exprText(kv.Key) {
				case "Status":
					if isLiteral {
						status, _ = strconv.Unquote(value.Value)
					}
				case "ErrorCode":
					if isLiteral {
						code, _ = strconv.Unquote(value.Value)
					}
				}
			}
			if status == "unknown" {
				unknownCodes++
				if _, known := runend.Codes[code]; !known {
					t.Errorf("UNKNOWN code %q has no run-end class", code)
				}
			}
			return true
		})
	}
	for _, code := range []string{runend.CodeOutcomeUnknown, runend.CodeBudgetEnded, runend.CodeProviderOutage} {
		if _, known := runend.Codes[code]; !known {
			t.Errorf("classifier code %q has no run-end class", code)
		}
	}
	if unknownCodes == 0 {
		t.Fatal("found no UNKNOWN answer in the service")
	}
}

// Every agent ceiling ends the run as a budget end, also when the bridge closed the conversation over it. A ledger
// fault beside a ceiling is a start-up check, so infrastructure; a parallel tool call is model behaviour and stays a
// plain UNKNOWN.
func TestAgentCeilingsAnswerRunBudgetExhausted(t *testing.T) {
	bridged := func(cause error) error {
		return providerTurnFailure{err: fmt.Errorf("%w; conversation closed: %w", providerbridge.ErrProviderTurnFailed,
			errors.Join(providerbridge.ErrClosed, fmt.Errorf("registered provider exchange failed; response withheld: %w", cause)))}
	}
	for _, tc := range []struct {
		name   string
		runErr error
		code   string
	}{
		{"reply cut at the completion cap", bridged(fmt.Errorf("capture OpenRouter response: %w: finish_reason length", runend.ErrCeiling)), runend.CodeBudgetEnded},
		{"history over the request bound", bridged(fmt.Errorf("provider response does not fit the next request: %w", runend.ErrCeiling)), runend.CodeBudgetEnded},
		{"predecessor context", fmt.Errorf("admit prompt: %w", providerbridge.ErrContextExhausted), runend.CodeBudgetEnded},
		{"ledger deadline", bridged(workflowrecovery.ErrDeadline), runend.CodeBudgetEnded},
		{"fault beside a ceiling", errors.Join(workflowrecovery.ErrCAS, runend.ErrCeiling), runend.CodeOutcomeUnknown},
		{"parallel tool call", bridged(openrouter.ErrParallelToolCall), runend.CodeOutcomeUnknown},
		{"unexplained", errors.New("cleanup failed"), runend.CodeOutcomeUnknown},
	} {
		if answer := unknownAnswerOf(t, tc.runErr); answer.ErrorCode != tc.code {
			t.Errorf("%s: answer %q, want %q", tc.name, answer.ErrorCode, tc.code)
		}
	}
}
