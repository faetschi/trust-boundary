//go:build linux

package main

import (
	"encoding/json"
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

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runend"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

// runEndSentinelDirs are the packages whose sentinels can reach a governed run's error: the service and session
// (this package), the provider bridge, broker and capture, the workflow ledger, the Pi runtime, and the workspace,
// session store, publication, private Git, audit, USD ledger and gate packages under them.
var runEndSentinelDirs = []string{".", "../../internal/providerbridge", "../../internal/broker",
	"../../internal/broker/openrouter", "../../internal/broker/protocol", "../../internal/workflowrecovery",
	"../../internal/piruntime", "../../internal/workspace", "../../internal/sessionrepo", "../../internal/publication",
	"../../internal/privategit", "../../internal/audit", "../../internal/usdledger", "../../internal/gate"}

// runEndBoundFiles are the provider path and the durable executor that returns tool results: every bound a failure
// message names there is a registry row.
var runEndBoundFiles = []string{"../../internal/providerbridge/*.go", "../../internal/broker/*.go",
	"../../internal/broker/openrouter/*.go", "durable_executor*.go"}

var runEndBoundWords = regexp.MustCompile(`(?i)exhaust|exceed|budget|limit|too many|too large|too long|ceiling|allowance|oversized|out of bounds|\bcap\b|\bbound\b`)

var runEndVerbs = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)

// isErrorValue reports an argument that names an error by the Go conventions: err, fooErr, ErrFoo.
func isErrorValue(e ast.Expr) bool {
	name := exprText(e)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name == "err" || strings.HasSuffix(name, "Err") || strings.HasPrefix(name, "Err")
}

// isSentinelName reports a package-level error variable: ErrFoo or errFoo.
func isSentinelName(name string) bool {
	return len(name) > 3 && (strings.HasPrefix(name, "Err") || strings.HasPrefix(name, "err")) && name[3] >= 'A' && name[3] <= 'Z'
}

func parseRunEndFiles(t *testing.T, pattern string) (*token.FileSet, []*ast.File) {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) == 0 {
		t.Fatalf("no Go files match %s: %v", pattern, err)
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

// runEndRow is a registry row read from its source: the err selector (pkg.ErrX, or ErrX of this package), the
// bound message, the package of a default row, whether the row is a carrier, and its class.
type runEndRow struct {
	err, bound, pkg, class string
	carrier                bool
}

func runEndRows(t *testing.T) []runEndRow {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "run_end_registry_linux.go", nil, 0)
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
				case "pkg":
					row.pkg, _ = strconv.Unquote(kv.Value.(*ast.BasicLit).Value)
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

// The registry fails closed: a source it does not class as infrastructure or model behaviour ends unclassified,
// never a rerun cause. This scan is a coverage aid on top of that. It walks the sources and fails on one without a
// row: every package-level error sentinel of the run's packages (a package may name a default row, which must be
// unclassified), every bound a failure message names on the provider path and in the durable executor, and every
// code of an UNKNOWN answer. An agent_ceiling bound carries runend.ErrCeiling at its site through %w, and no other
// bound carries it, so the classifier sees exactly the registered ceilings. On those paths a failure wraps its cause
// with %w, so no wrap drops the marker on its way to the classifier.
func TestRunEndRegistryClassesEverySource(t *testing.T) {
	rows := runEndRows(t)
	sentinelRows, boundRows, packageRows := map[string]bool{}, map[string]runEndRow{}, map[string]bool{}
	for _, row := range rows {
		switch {
		case row.err != "":
			sentinelRows[row.err] = true
		case row.bound != "":
			boundRows[row.bound] = row
		case row.pkg != "":
			packageRows[row.pkg] = true
			if row.class != "runend.Unclassified" {
				t.Errorf("default row of package %s is %s; a default must be unclassified", row.pkg, row.class)
			}
		}
		if !strings.HasPrefix(row.class, "runend.") {
			t.Errorf("row %+v has no class", row)
		}
	}

	for _, dir := range runEndSentinelDirs {
		_, files := parseRunEndFiles(t, filepath.Join(dir, "*.go"))
		for _, file := range files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.VAR {
					continue
				}
				for _, spec := range gen.Specs {
					for _, name := range spec.(*ast.ValueSpec).Names {
						if !isSentinelName(name.Name) {
							continue
						}
						qualified := file.Name.Name + "." + name.Name
						if file.Name.Name == "main" {
							qualified = name.Name
						}
						if !sentinelRows[qualified] && !packageRows[file.Name.Name] {
							t.Errorf("sentinel %s has no run-end class", qualified)
						}
					}
				}
			}
		}
	}

	seenBounds := map[string]bool{}
	for _, pattern := range runEndBoundFiles {
		fset, files := parseRunEndFiles(t, pattern)
		for _, file := range files {
			// A sentinel's own initialiser is classed by its sentinel row; every other call in the file is walked,
			// package-level initialisers included.
			sentinelInit := map[ast.Node]bool{}
			for _, decl := range file.Decls {
				if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
					for _, spec := range gen.Specs {
						value := spec.(*ast.ValueSpec)
						for i, name := range value.Names {
							if isSentinelName(name.Name) && i < len(value.Values) {
								sentinelInit[value.Values[i]] = true
							}
						}
					}
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 || sentinelInit[call] {
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
				marked := false
				if fun == "fmt.Errorf" {
					arg := 0
					for _, verb := range runEndVerbs.FindAllString(message, -1) {
						if verb == "%%" {
							continue
						}
						arg++
						if arg >= len(call.Args) {
							break
						}
						if verb == "%v" && isErrorValue(call.Args[arg]) {
							t.Errorf("%s: %q formats an error with %%v; wrap it with %%w", fset.Position(call.Pos()), message)
						}
						if exprText(call.Args[arg]) == "runend.ErrCeiling" {
							marked = verb == "%w"
							if !marked {
								t.Errorf("%s: %q carries runend.ErrCeiling with %s, not %%w", fset.Position(call.Pos()), message, verb)
							}
						}
					}
				}
				if !runEndBoundWords.MatchString(message) {
					return true
				}
				seenBounds[message] = true
				row, registered := boundRows[message]
				if !registered {
					t.Errorf("%s: bound %q has no run-end class", fset.Position(call.Pos()), message)
					return true
				}
				if ceiling := row.class == "runend.AgentCeiling" && !row.carrier; ceiling != marked {
					t.Errorf("%s: bound %q is %s but carries runend.ErrCeiling=%t", fset.Position(call.Pos()), message, row.class, marked)
				}
				return true
			})
		}
	}
	for message := range boundRows {
		if !seenBounds[message] {
			t.Errorf("bound row %q names no failure message on the scanned paths", message)
		}
	}

	// Every code of an UNKNOWN answer: a literal in a piServiceResponse whose status is "unknown", or a code the
	// classifier chooses (runEndAnswer, which returns only runend codes).
	_, files := parseRunEndFiles(t, "*.go")
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
	for _, code := range []string{runend.CodeOutcomeUnknown, runend.CodeBudgetEnded, runend.CodeProviderOutage, runend.CodeUnclassified,
		runend.CodeLaunchNotAdmitted} {
		if _, known := runend.Codes[code]; !known {
			t.Errorf("classifier code %q has no run-end class", code)
		}
	}
	if unknownCodes == 0 {
		t.Fatal("found no UNKNOWN answer in the service")
	}
}

// Every agent ceiling ends the run as a budget end, also when the bridge closed the conversation over it. A ledger
// fault beside a ceiling is a start-up check, so infrastructure. A disposed provider turn failure (a parallel tool
// call, a refused reply) is model behaviour, a rerun cause. Anything the registry does not class as infrastructure
// or model behaviour ends unclassified, never a rerun cause.
func TestAgentCeilingsAnswerRunBudgetExhausted(t *testing.T) {
	bridged := func(cause error) error {
		return governedChannelsError(nil, fmt.Errorf("%w; conversation closed: %w", providerbridge.ErrProviderTurnFailed,
			errors.Join(providerbridge.ErrClosed, fmt.Errorf("registered provider exchange failed; response withheld: %w", cause))))
	}
	executorFailed := func(cause error) error {
		return governedChannelsError(fmt.Errorf("executor failed; result withheld: %w", cause), providerbridge.ErrClosed)
	}
	for _, tc := range []struct {
		name   string
		runErr error
		code   string
	}{
		{"reply cut at the completion cap", bridged(fmt.Errorf("capture OpenRouter response: %w: finish reason length", runend.ErrCeiling)), runend.CodeBudgetEnded},
		{"history over the request bound", bridged(fmt.Errorf("provider response does not fit the next request: %w", runend.ErrCeiling)), runend.CodeBudgetEnded},
		{"predecessor context", fmt.Errorf("admit prompt: %w", providerbridge.ErrContextExhausted), runend.CodeBudgetEnded},
		{"ledger deadline", bridged(workflowrecovery.ErrDeadline), runend.CodeBudgetEnded},
		{"workspace limit", executorFailed(fmt.Errorf("durable write: %w", workspace.ErrLimit)), runend.CodeBudgetEnded},
		{"start-up fault beside a ceiling", errors.Join(workflowrecovery.ErrRollback, runend.ErrCeiling), runend.CodeOutcomeUnknown},
		{"clock fault beside a ceiling", errors.Join(workflowrecovery.ErrClockUncertain, runend.ErrCeiling), runend.CodeOutcomeUnknown},
		{"ledger verifier over a ceiling", fmt.Errorf("%w: progress verifier: %w", workflowrecovery.ErrObservation, runend.ErrCeiling), runend.CodeBudgetEnded},
		{"CAS beside a ceiling", errors.Join(workflowrecovery.ErrCAS, runend.ErrCeiling), runend.CodeBudgetEnded},
		{"parallel tool call", bridged(openrouter.ErrParallelToolCall), runend.CodeOutcomeUnknown},
		{"refused reply", bridged(errors.New("captured response model differs from registered model")), runend.CodeOutcomeUnknown},
		{"command never started", executorFailed(protocol.ErrCommandNotStarted), runend.CodeOutcomeUnknown},
		{"unexplained", errors.New("cleanup failed"), runend.CodeUnclassified},
		{"provider turn failure beside a cleanup failure", errors.Join(bridged(openrouter.ErrParallelToolCall),
			errors.New("close settled production session before receipt: synthetic")), runend.CodeUnclassified},
		{"executor failure without a class", executorFailed(errors.New("durable read failed")), runend.CodeUnclassified},
	} {
		if answer := unknownAnswerOf(t, tc.runErr); answer.ErrorCode != tc.code {
			t.Errorf("%s: answer %q, want %q", tc.name, answer.ErrorCode, tc.code)
		}
	}
}

// A real chain from the failing site: an allowed write over the store's file-size limit fails in the session store,
// the durable executor and the provider bridge return it, and the governed loops join it the way the session does.
// The run answers run-budget-exhausted: the agent's own write met a workspace ceiling, never a rerun cause.
func TestWorkspaceLimitThroughTheExecutorIsABudgetEnd(t *testing.T) {
	limits := workspace.DefaultLimits()
	limits.MaxFileBytes = 64
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true, storeLimits: &limits,
		rules: []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow}}})
	content := strings.Repeat("x", 200)
	f.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewDevelopmentOfflineConversation(config, journal, []providerbridge.DevelopmentProviderTurn{
			{ResponseID: "limit-response-write", CallID: "limit-call-write", Tool: "write",
				Arguments: json.RawMessage(`{"path":"task.txt","content":"` + content + `"}`)},
		})
	})
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("write turn: turn=%+v err=%v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID: turn.ToolCall.ID, Tool: turn.ToolCall.Name, Arguments: turn.ToolCall.Arguments}
	matched, err := f.conversation.Correlate(f.ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate: %+v %v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, f.policy)
	var execErr error
	if execErr = f.conversation.RecordDecision(f.ctx, proposal, decision); execErr == nil {
		_, execErr = executor.Execute(f.ctx, proposal, decision)
	}
	if !errors.Is(execErr, workspace.ErrLimit) {
		t.Fatalf("an allowed write over the file-size limit: verdict %s, err %v", decision.Verdict, execErr)
	}
	runErr := errors.Join(fmt.Errorf("governed production channels failed before the turn ended: %w",
		governedChannelsError(fmt.Errorf("executor failed; result withheld: %w", execErr), nil)))
	if answer := unknownAnswerOf(t, runErr); answer.ErrorCode != runend.CodeBudgetEnded {
		t.Fatalf("answer %+v; run error %v", answer, runErr)
	}
}
