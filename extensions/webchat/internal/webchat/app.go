package webchat

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// distFS is the committed Vite output for the React client
// (extensions/webchat/internal/webchat/dist, built from extensions/webchat/frontend).
// The `all:` prefix keeps the dot-directory .vite/, which holds the manifest the
// route table is derived from. The Go build needs no Node.
//
//go:embed all:dist
var distFS embed.FS

const (
	appPrefix   = "/app/"
	appIndex    = "index.html"
	appManifest = ".vite/manifest.json"
)

// appFilePattern admits only the file shapes this build emits: JavaScript and
// CSS under assets/. Any other file the manifest names fails the derivation.
var appFilePattern = regexp.MustCompile(`^assets/[A-Za-z0-9][A-Za-z0-9._-]*\.(js|css)$`)

// appShellReference matches the URL attributes of the shell. Each one must name
// a derived route.
var appShellReference = regexp.MustCompile(`\b(?:src|href)="([^"]*)"`)

// viteManifestEntry is the part of a Vite manifest entry that the route table is
// derived from. Every other field is ignored.
type viteManifestEntry struct {
	File           string   `json:"file"`
	IsEntry        bool     `json:"isEntry"`
	CSS            []string `json:"css"`
	Assets         []string `json:"assets"`
	Imports        []string `json:"imports"`
	DynamicImports []string `json:"dynamicImports"`
}

// appRoute serves one file. name is relative to the app source.
type appRoute struct {
	name        string
	contentType string
}

// appBundle is the /app/ route table. It is derived from the Vite manifest at
// startup: index.html plus every file the manifest reaches from an entry. A file
// that is merely present in the build output is not served. When derivation
// fails, err says why, /app/ answers 503, and the rest of the gateway is
// unaffected.
type appBundle struct {
	source fs.FS
	routes map[string]appRoute // exact escaped request path -> file
	err    error
}

// embeddedApp derives the route table from the committed build.
func embeddedApp() *appBundle {
	source, err := fs.Sub(distFS, "dist")
	if err != nil {
		return &appBundle{err: fmt.Errorf("embedded app unavailable: %w", err)}
	}
	return loadAppBundle(source)
}

// AppStatus reports why /app/ cannot be served from the embedded build, or nil
// when it can. The vanilla UI at / is not affected by this status.
func AppStatus() error {
	return embeddedApp().err
}

func loadAppBundle(source fs.FS) *appBundle {
	routes, err := deriveAppRoutes(source)
	return &appBundle{source: source, routes: routes, err: err}
}

// deriveAppRoutes is the closed allowlist for /app/. It starts from index.html and
// the manifest entries marked isEntry, follows css, assets, imports and dynamic
// imports, and admits exactly those files. Every admitted file must exist and be
// a permitted type, and every URL in the shell must be admitted.
func deriveAppRoutes(source fs.FS) (map[string]appRoute, error) {
	raw, err := fs.ReadFile(source, appManifest)
	if err != nil {
		return nil, fmt.Errorf("app manifest %s is missing: %w", appManifest, err)
	}
	var manifest map[string]viteManifestEntry
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("app manifest %s is corrupt: %w", appManifest, err)
	}
	if shell, ok := manifest[appIndex]; !ok || !shell.IsEntry {
		return nil, fmt.Errorf("app manifest has no entry for %s", appIndex)
	}

	files := map[string]bool{appIndex: true}
	visited := map[string]bool{}
	var visit func(key string) error
	visit = func(key string) error {
		if visited[key] {
			return nil
		}
		visited[key] = true
		entry, ok := manifest[key]
		if !ok {
			return fmt.Errorf("app manifest references %q, which has no entry", key)
		}
		if entry.File != "" {
			files[entry.File] = true
		}
		for _, name := range entry.CSS {
			files[name] = true
		}
		for _, name := range entry.Assets {
			files[name] = true
		}
		for _, next := range entry.Imports {
			if err := visit(next); err != nil {
				return err
			}
		}
		for _, next := range entry.DynamicImports {
			if err := visit(next); err != nil {
				return err
			}
		}
		return nil
	}
	entryKeys := make([]string, 0, len(manifest))
	for key, entry := range manifest {
		if entry.IsEntry {
			entryKeys = append(entryKeys, key)
		}
	}
	sort.Strings(entryKeys)
	for _, key := range entryKeys {
		if err := visit(key); err != nil {
			return nil, err
		}
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	routes := make(map[string]appRoute, len(names))
	for _, name := range names {
		contentType, err := appContentType(name)
		if err != nil {
			return nil, err
		}
		info, err := fs.Stat(source, name)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("app manifest lists %s, which is missing from the build", name)
		}
		routes[appPath(name)] = appRoute{name: name, contentType: contentType}
	}

	shell, err := fs.ReadFile(source, appIndex)
	if err != nil {
		return nil, fmt.Errorf("app shell %s is missing: %w", appIndex, err)
	}
	for _, match := range appShellReference.FindAllSubmatch(shell, -1) {
		reference := string(match[1])
		if _, ok := routes[reference]; !ok {
			return nil, fmt.Errorf("app shell references %q, which the manifest does not derive", reference)
		}
	}
	return routes, nil
}

// appPath is the request path under which a derived file is served.
func appPath(name string) string {
	if name == appIndex {
		return appPrefix
	}
	return appPrefix + name
}

func appContentType(name string) (string, error) {
	switch {
	case name == appIndex:
		return "text/html; charset=utf-8", nil
	case !appFilePattern.MatchString(name):
		return "", fmt.Errorf("app manifest lists %q, which is not a permitted JavaScript or CSS asset path", name)
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8", nil
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8", nil
	default:
		return "", fmt.Errorf("app manifest lists %q, which has an unsupported type", name)
	}
}
