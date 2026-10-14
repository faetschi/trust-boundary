package webchat

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
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

// appBootScript is the theme script the shell loads before the first paint. Vite copies
// frontend/public/ unchanged, so the manifest does not name it. admitBootScript admits it instead.
const appBootScript = "theme-boot.js"

// appFilePattern admits only the file shapes this build emits: JavaScript and
// CSS under assets/. Any other file the manifest names fails the derivation.
var appFilePattern = regexp.MustCompile(`^assets/[A-Za-z0-9][A-Za-z0-9._-]*\.(js|css)$`)

// shellURLAttributes are the attributes whose whole value the browser resolves
// as one URL. srcset and imagesrcset hold several candidates and are split
// separately. A value with more than one URL (ping, archive) is not admitted.
var shellURLAttributes = map[string]bool{
	"action": true, "archive": true, "background": true, "cite": true, "classid": true,
	"codebase": true, "data": true, "dynsrc": true, "formaction": true, "href": true,
	"icon": true, "longdesc": true, "lowsrc": true, "manifest": true, "ping": true,
	"poster": true, "src": true, "usemap": true, "xlink:href": true,
}

// shellRawTextElements hold text, not markup, up to their end tag. Neither the
// browser nor the derivation reads tags inside them.
var shellRawTextElements = map[string]bool{
	"iframe": true, "noembed": true, "noframes": true, "noscript": true,
	"script": true, "style": true, "textarea": true, "title": true, "xmp": true,
}

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
// when it can. When it is non-nil, / serves the legacy workspace as a fallback;
// /legacy/ is not affected by this status.
func AppStatus() error {
	return embeddedApp().err
}

func loadAppBundle(source fs.FS) *appBundle {
	routes, err := deriveAppRoutes(source)
	return &appBundle{source: source, routes: routes, err: err}
}

// deriveAppRoutes is the closed allowlist for /app/. It starts from index.html and
// the manifest entries marked isEntry, follows css, assets, imports and dynamic
// imports, and admits exactly those files, plus the theme script when the shell loads
// it first (admitBootScript). Every admitted file must exist and be a permitted type,
// and every URL in the shell must be admitted.
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
	if err := admitBootScript(shell, source, routes); err != nil {
		return nil, err
	}
	if err := admitShell(shell, routes); err != nil {
		return nil, err
	}
	return routes, nil
}

// admitBootScript admits the theme script when the shell loads it, and refuses every other way of loading it. The
// browser runs the script before the first paint, so the shell may load it only as its first <script>: a classic
// script whose only attribute is src (no type, defer, async or nomodule), before <body>, and as its only reference.
// A shell that does not name it admits nothing, so the file stays unserved.
func admitBootScript(shell []byte, source fs.FS, routes map[string]appRoute) error {
	tags, err := parseShellTags(shell)
	if err != nil {
		return err
	}
	const bootURL = appPrefix + appBootScript
	references := 0
	var first *shellTag
	bodyFirst := false
	for i := range tags {
		tag := &tags[i]
		if first == nil && tag.name == "body" {
			bodyFirst = true
		}
		if first == nil && tag.name == "script" {
			first = tag
		}
		for _, attr := range tag.attrs {
			for _, url := range shellAttrURLs(attr) {
				if url == bootURL {
					references++
				}
			}
		}
	}
	if references == 0 {
		return nil
	}
	notFirst := fmt.Errorf("app shell loads %s other than as its first, classic script before <body>", bootURL)
	if first == nil || bodyFirst || references != 1 {
		return notFirst
	}
	if len(first.attrs) != 1 || first.attrs[0].name != "src" || strings.Trim(first.attrs[0].value, " \t\n\f\r") != bootURL {
		return notFirst
	}
	info, err := fs.Stat(source, appBootScript)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("app shell loads %s, which is missing from the build", bootURL)
	}
	routes[bootURL] = appRoute{name: appBootScript, contentType: "text/javascript; charset=utf-8"}
	return nil
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

// admitShell is the shell's allowlist. Every URL the browser would load from the
// shell must be an admitted route. A <base> element, a <meta http-equiv>, an
// inline <script>, and any tag the tokenizer cannot read are refused: each can
// change what is loaded, or hides what is loaded from the check.
func admitShell(shell []byte, routes map[string]appRoute) error {
	tags, err := parseShellTags(shell)
	if err != nil {
		return err
	}
	admit := func(reference string) error {
		if _, ok := routes[reference]; !ok {
			return fmt.Errorf("app shell references %q, which the manifest does not derive", reference)
		}
		return nil
	}
	for _, tag := range tags {
		switch tag.name {
		case "base":
			return errors.New("app shell has a <base> element, which is not admitted")
		case "script":
			if strings.TrimSpace(tag.body) != "" {
				return errors.New("app shell has an inline <script> body")
			}
		}
		hasSource := false
		for _, attr := range tag.attrs {
			if tag.name == "meta" && attr.name == "http-equiv" {
				return errors.New("app shell has a <meta http-equiv>, which is not admitted")
			}
			for _, reference := range shellAttrURLs(attr) {
				if err := admit(reference); err != nil {
					return err
				}
			}
			if tag.name == "script" && attr.name == "src" {
				hasSource = true
			}
		}
		if tag.name == "script" && !hasSource {
			return errors.New("app shell has an inline <script> (no src)")
		}
	}
	return nil
}

// shellAttrURLs returns the URLs an attribute names, as the browser reads them: each candidate of a srcset, and
// the whole value of a URL attribute. Any other attribute names no URL.
func shellAttrURLs(attr shellAttr) []string {
	switch {
	case attr.name == "srcset" || attr.name == "imagesrcset":
		var urls []string
		for _, candidate := range strings.Split(attr.value, ",") {
			if fields := strings.Fields(candidate); len(fields) > 0 {
				urls = append(urls, fields[0])
			}
		}
		return urls
	case shellURLAttributes[attr.name]:
		return []string{strings.Trim(attr.value, " \t\n\f\r")}
	}
	return nil
}

// shellTag is one start tag of the app shell. Names are lower-cased. Attribute
// values are raw: character references are not decoded, so an encoded URL is
// not an admitted route.
type shellTag struct {
	name  string
	attrs []shellAttr
	body  string // the text of a raw-text element, such as a <script>
}

type shellAttr struct {
	name  string
	value string
}

// parseShellTags reads the shell the way the browser reads it for the parts that
// decide what is loaded. Comments and declarations are skipped, raw-text bodies
// are not read as markup, and every start tag is returned with its attributes.
// Anything it cannot read is an error, so the caller fails closed.
func parseShellTags(shell []byte) ([]shellTag, error) {
	var tags []shellTag
	i := 0
	for {
		offset := bytes.IndexByte(shell[i:], '<')
		if offset < 0 {
			return tags, nil
		}
		i += offset
		rest := shell[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest[len("<!--"):], []byte("-->"))
			if end < 0 {
				return nil, errors.New("app shell has an unterminated HTML comment")
			}
			i += len("<!--") + end + len("-->")
		case len(rest) > 1 && (rest[1] == '!' || rest[1] == '?' || rest[1] == '/'):
			end := bytes.IndexByte(rest, '>')
			if end < 0 {
				return nil, errors.New("app shell has an unterminated declaration or end tag")
			}
			i += end + 1
		case len(rest) > 1 && isASCIIAlpha(rest[1]):
			tag, next, err := parseShellStartTag(shell, i)
			if err != nil {
				return nil, err
			}
			i = next
			if shellRawTextElements[tag.name] {
				body, end, err := rawTextBody(shell, i, tag.name)
				if err != nil {
					return nil, err
				}
				tag.body = body
				i = end
			}
			tags = append(tags, tag)
		default:
			i++
		}
	}
}

// parseShellStartTag reads the start tag at start. Quotes, unquoted values,
// whitespace and '/' between attributes follow the browser's tokenizer.
func parseShellStartTag(shell []byte, start int) (shellTag, int, error) {
	i := start + 1
	nameStart := i
	for i < len(shell) && isTagNameByte(shell[i]) {
		i++
	}
	tag := shellTag{name: strings.ToLower(string(shell[nameStart:i]))}
	for {
		for i < len(shell) && (isHTMLSpace(shell[i]) || shell[i] == '/') {
			i++
		}
		if i >= len(shell) {
			return shellTag{}, 0, fmt.Errorf("app shell has an unterminated <%s> tag", tag.name)
		}
		if shell[i] == '>' {
			return tag, i + 1, nil
		}
		nameStart = i
		for i < len(shell) && !isHTMLSpace(shell[i]) && shell[i] != '/' && shell[i] != '>' && shell[i] != '=' {
			if shell[i] == '"' || shell[i] == '\'' || shell[i] == '<' {
				return shellTag{}, 0, fmt.Errorf("app shell has a malformed attribute name in <%s>", tag.name)
			}
			i++
		}
		if i == nameStart {
			return shellTag{}, 0, fmt.Errorf("app shell has a malformed attribute in <%s>", tag.name)
		}
		attr := shellAttr{name: strings.ToLower(string(shell[nameStart:i]))}
		next := i
		for next < len(shell) && isHTMLSpace(shell[next]) {
			next++
		}
		if next < len(shell) && shell[next] == '=' {
			next++
			for next < len(shell) && isHTMLSpace(shell[next]) {
				next++
			}
			if next >= len(shell) {
				return shellTag{}, 0, fmt.Errorf("app shell has an unterminated <%s> tag", tag.name)
			}
			if quote := shell[next]; quote == '"' || quote == '\'' {
				end := bytes.IndexByte(shell[next+1:], quote)
				if end < 0 {
					return shellTag{}, 0, fmt.Errorf("app shell has an unterminated quoted attribute in <%s>", tag.name)
				}
				attr.value = string(shell[next+1 : next+1+end])
				i = next + 1 + end + 1
			} else {
				valueStart := next
				for next < len(shell) && !isHTMLSpace(shell[next]) && shell[next] != '>' {
					if bytes.IndexByte([]byte("\"'<=`"), shell[next]) >= 0 {
						return shellTag{}, 0, fmt.Errorf("app shell has a malformed unquoted attribute in <%s>", tag.name)
					}
					next++
				}
				attr.value = string(shell[valueStart:next])
				i = next
			}
		}
		tag.attrs = append(tag.attrs, attr)
	}
}

// rawTextBody returns the text from `from` up to the end tag of a raw-text
// element, and the index of that end tag.
func rawTextBody(shell []byte, from int, name string) (string, int, error) {
	lower := bytes.ToLower(shell[from:])
	needle := []byte("</" + name)
	search := 0
	for {
		offset := bytes.Index(lower[search:], needle)
		if offset < 0 {
			return "", 0, fmt.Errorf("app shell has an unterminated <%s> element", name)
		}
		at := search + offset
		end := at + len(needle)
		if end < len(lower) && (isHTMLSpace(lower[end]) || lower[end] == '/' || lower[end] == '>') {
			return string(shell[from : from+at]), from + at, nil
		}
		search = end
	}
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isTagNameByte reports whether b can be part of a tag name. The browser ends a
// name only at whitespace, '/' or '>'.
func isTagNameByte(b byte) bool {
	return !isHTMLSpace(b) && b != '/' && b != '>'
}

func isHTMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\f' || b == '\r'
}
