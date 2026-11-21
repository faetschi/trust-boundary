package e2e

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This guard enforces the layer map in docs/design/supervisor-ownership.md. It reads the real import declarations of
// non-test files on every platform and build tag, so a forbidden import fails here on any host.

const modulePrefix = "tbound/supervisor/"

const (
	layerShared  = "shared"
	layerProduct = "product"
	layerEval    = "evaluation"
	layerCommand = "command"
)

var layerPackages = map[string][]string{
	layerShared: {"audit", "broker", "broker/correlation", "broker/openrouter", "broker/protocol", "cellview", "delta", "drainproof",
		"effectoracle", "faultfs", "gate", "ipc", "piinstall", "piruntime", "privategit", "providerbridge", "publication",
		"publicationevents", "runend", "sandbox", "sessionlaunch", "sessionrepo", "workflow", "workflowrecovery", "workspace", "wsoracle"},
	layerProduct: {"productdriver", "productprotocol", "productprovider", "productsession", "productsetup", "projectbundle", "releasetrust"},
	layerEval: {"adaptivesource", "attacker", "calibration", "cgroupguard", "corpusadmit", "evalcorpus", "evalreport", "evalscore",
		"guestevidence", "netoracle", "panelaudit", "procoracle", "researchprofile", "researchroute", "routematrix",
		"runbooklint", "runner", "taskaccept", "trialfix", "usdledger", "vrun", "e2e"},
	layerCommand: {"cmd/tbound", "cmd/tbound-cell-entrypoint", "cmd/tbound-doctor", "cmd/tbound-hint-eval", "cmd/tbound-panel",
		"cmd/tbound-profile", "cmd/tbound-provider", "cmd/tbound-run"},
}

// piruntime may import these product packages, but only from product_*_linux.go files.
var piruntimeProductImports = map[string]bool{"productsetup": true, "productsession": true}

// productdriver may import only these packages of this module directly.
var productdriverImports = map[string]bool{"audit": true, "broker/protocol": true, "productprotocol": true, "productsession": true, "publication": true}

func layerIndex(t *testing.T) map[string]string {
	t.Helper()
	index := map[string]string{}
	for layer, pkgs := range layerPackages {
		for _, pkg := range pkgs {
			if prev, dup := index[pkg]; dup {
				t.Fatalf("package %s is listed in both %s and %s", pkg, prev, layer)
			}
			index[pkg] = layer
		}
	}
	return index
}

// packageName maps a module import path to its name in the lists: internal/x -> x, cmd/x and e2e unchanged.
func packageName(importPath string) (string, bool) {
	rest, ok := strings.CutPrefix(importPath, modulePrefix)
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(rest, "internal/"), true
}

// checkSource parses the imports of one non-test file and returns one line per violation: file:line: source -> destination: rule.
func checkSource(rel, readPath, source string, src any, layers map[string]string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, readPath, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	srcLayer, known := layers[source]
	if !known {
		return []string{rel + ": " + source + ": unknown package, assign it to a layer"}, nil
	}
	var out []string
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		dest, inModule := packageName(importPath)
		if !inModule {
			continue
		}
		report := func(rule string) {
			out = append(out, rel+":"+strconv.Itoa(fset.Position(spec.Pos()).Line)+": "+source+" -> "+dest+": "+rule)
		}
		destLayer, known := layers[dest]
		if !known {
			report("unknown package, assign it to a layer")
			continue
		}
		if (srcLayer == layerShared || srcLayer == layerProduct) && destLayer == layerEval {
			report(srcLayer + " packages must not import evaluation packages")
		}
		if srcLayer == layerShared && destLayer == layerProduct {
			allowedName, _ := path.Match("product_*_linux.go", path.Base(rel))
			if !(source == "piruntime" && allowedName && piruntimeProductImports[dest]) {
				report("shared packages must not import product packages (piruntime only from product_*_linux.go, only productsetup and productsession)")
			}
		}
		if source == "productdriver" && !productdriverImports[dest] {
			report("productdriver may import only audit, broker/protocol, productprotocol, productsession and publication")
		}
	}
	return out, nil
}

func TestImportBoundaries(t *testing.T) {
	layers := layerIndex(t)
	root := ".."
	seen := map[string]bool{}
	var violations []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		source := strings.TrimPrefix(path.Dir(rel), "internal/")
		seen[source] = true
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		found, err := checkSource(rel, p, source, nil, layers)
		if err != nil {
			return err
		}
		violations = append(violations, found...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for pkg := range layers {
		if !seen[pkg] {
			violations = append(violations, pkg+": listed in a layer but no such package directory")
		}
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Error(v)
	}
}

func TestImportBoundariesNegativeCases(t *testing.T) {
	layers := layerIndex(t)
	for _, tc := range []struct {
		name, rel, source, src, want string
	}{
		{"forbidden evaluation import", "internal/publication/x.go", "publication",
			"package p\n\nimport \"tbound/supervisor/internal/attacker\"\n",
			"internal/publication/x.go:3: publication -> attacker: shared packages must not import evaluation packages"},
		{"allowed product filename", "internal/piruntime/product_x_linux.go", "piruntime",
			"package p\n\nimport _ \"tbound/supervisor/internal/productsetup\"\n", ""},
		{"same import under forbidden filename", "internal/piruntime/containment_x_linux.go", "piruntime",
			"package p\n\nimport _ \"tbound/supervisor/internal/productsetup\"\n",
			"internal/piruntime/containment_x_linux.go:3: piruntime -> productsetup: shared packages must not import product packages"},
		{"piruntime product file importing another product package", "internal/piruntime/product_x_linux.go", "piruntime",
			"package p\n\nimport _ \"tbound/supervisor/internal/productdriver\"\n", "piruntime -> productdriver"},
		{"forbidden driver import", "internal/productdriver/x.go", "productdriver",
			"package p\n\nimport _ \"tbound/supervisor/internal/sandbox\"\n",
			"internal/productdriver/x.go:3: productdriver -> sandbox: productdriver may import only"},
		{"unknown package", "internal/newthing/x.go", "newthing", "package p\n", "newthing: unknown package"},
		{"aliased import", "internal/publication/x.go", "publication",
			"package p\n\nimport (\n\t\"fmt\"\n\tatk \"tbound/supervisor/internal/attacker\"\n)\n",
			"internal/publication/x.go:5: publication -> attacker"},
		{"import only in a comment", "internal/publication/x.go", "publication",
			"package p\n\n// import \"tbound/supervisor/internal/attacker\"\n/* import \"tbound/supervisor/internal/attacker\" */\nimport \"fmt\"\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkSource(tc.rel, tc.rel, tc.source, tc.src, layers)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(got, "\n")
			if tc.want == "" && joined != "" || !strings.Contains(joined, tc.want) {
				t.Fatalf("got %q, want substring %q", joined, tc.want)
			}
		})
	}
}
