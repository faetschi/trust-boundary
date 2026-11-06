//go:build linux

package piruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"testing"
)

// productD09CallSite pins the source expressions that give a product-reachable Podman or cosign child its environment.
// Run and worker launch need a delegated cgroup and a sealed command view, so the dynamic test cannot reach every call site
// on every host. This guard fails on any change to these expressions, including a revert to d09ProbeEnv().
type productD09CallSite struct {
	file     string
	function string // "Type.Method" for a method, "name" for a function
	returns  bool   // also pin the return expressions, which select the environment
	want     []string
}

// productD09EnvironmentHelpers are the Podman helpers whose last argument is the child environment.
var productD09EnvironmentHelpers = map[string]bool{"inspectContainer": true, "removeContainer": true, "containerExists": true}

func productD09FunctionKey(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return function.Name.Name
	}
	receiver := function.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	return types.ExprString(receiver) + "." + function.Name.Name
}

// productD09EnvironmentExpressions returns the sorted environment expressions of one function: assignments to an Env field,
// the last argument of each environment helper, the environment field of a composite literal, and, when requested, returns.
func productD09EnvironmentExpressions(t *testing.T, site productD09CallSite) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), site.file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	matched := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil || productD09FunctionKey(function) != site.function {
			continue
		}
		matched = true
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.AssignStmt:
				for index, target := range typed.Lhs {
					if selector, ok := target.(*ast.SelectorExpr); ok && selector.Sel.Name == "Env" && index < len(typed.Rhs) {
						found = append(found, "Env="+types.ExprString(typed.Rhs[index]))
					}
				}
			case *ast.CallExpr:
				if helper, ok := typed.Fun.(*ast.Ident); ok && productD09EnvironmentHelpers[helper.Name] && len(typed.Args) > 0 {
					found = append(found, "arg:"+helper.Name+"="+types.ExprString(typed.Args[len(typed.Args)-1]))
				}
			case *ast.KeyValueExpr:
				if key, ok := typed.Key.(*ast.Ident); ok && key.Name == "environment" {
					found = append(found, "literal:environment="+types.ExprString(typed.Value))
				}
			case *ast.ReturnStmt:
				if site.returns {
					for _, result := range typed.Results {
						found = append(found, "return="+types.ExprString(result))
					}
				}
			}
			return true
		})
	}
	if !matched {
		t.Fatalf("%s has no function %s", site.file, site.function)
	}
	slices.Sort(found)
	return found
}

// TestProductD09EnvironmentCallSitesCarryTheProductEnvironment is static: it pins every production expression that feeds a
// Podman or cosign child's environment, so a temporary revert of any one of them fails here on every host.
func TestProductD09EnvironmentCallSitesCarryTheProductEnvironment(t *testing.T) {
	for _, site := range []productD09CallSite{
		{file: "containment_config_linux.go", function: "execD09CommandRunner.Run", want: []string{
			"Env=r.commandEnvironment()",
		}},
		{file: "containment_config_linux.go", function: "execD09CommandRunner.commandEnvironment", returns: true, want: []string{
			"return=r.environment", "return=d09ProbeEnv()",
		}},
		{file: "product_d09_launcher_linux.go", function: "D09PodmanCrunLauncher.commandEnvironment", returns: true, want: []string{
			"return=productsetup.CommandCellEnvironment()", "return=d09ProbeEnv()",
		}},
		{file: "containment_launch_linux.go", function: "D09PodmanCrunLauncher.StartVerifiedWithProof", want: []string{
			"Env=l.commandEnvironment()", "arg:inspectContainer=l.commandEnvironment()",
		}},
		{file: "containment_settle_linux.go", function: "D09PodmanCrunLauncher.cleanupContainer", want: []string{
			"Env=l.commandEnvironment()",
		}},
		{file: "d09_command_runner_linux.go", function: "D09CommandRunner.Run", want: []string{
			"Env=r.launcher.commandEnvironment()", "Env=r.launcher.commandEnvironment()",
			"arg:inspectContainer=r.launcher.commandEnvironment()", "arg:inspectContainer=r.launcher.commandEnvironment()",
			"arg:removeContainer=r.launcher.commandEnvironment()", "arg:containerExists=r.launcher.commandEnvironment()",
		}},
		{file: "d09_command_runner_linux.go", function: "D09CommandRunner.cleanupCommand", want: []string{
			"Env=r.launcher.commandEnvironment()",
		}},
		{file: "d09_command_runner_linux.go", function: "inspectContainer", want: []string{"Env=environment"}},
		{file: "d09_command_runner_linux.go", function: "removeContainer", want: []string{"Env=environment"}},
		{file: "d09_command_runner_linux.go", function: "containerExists", want: []string{"Env=environment"}},
		{file: "product_d09_host_linux.go", function: "productD09VerifyArtifacts", want: []string{
			"literal:environment=productsetup.CommandCellEnvironment()",
		}},
	} {
		want := slices.Clone(site.want)
		slices.Sort(want)
		if got := productD09EnvironmentExpressions(t, site); !slices.Equal(got, want) {
			t.Errorf("%s %s environment expressions:\n got %q\nwant %q", site.file, site.function, got, want)
		}
	}
}
