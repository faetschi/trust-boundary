package taskaccept

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"tbound/supervisor/internal/trialfix"
)

var (
	modeRE   = regexp.MustCompile(`^0[0-7]{3}$`)
	digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// validPath applies the canonical-path rules of a fixture spec (internal/trialfix): relative,
// no empty, "." or ".." component, no control character or backslash, valid UTF-8, bounded
// length and depth, and not under the filesystem's lost+found.
func validPath(p string) error {
	bad := func(why string) error { return fmt.Errorf("path %q %s", p, why) }
	if p == "" || len(p) > trialfix.MaxPathBytes || !utf8.ValidString(p) {
		return bad("is empty, too long or not UTF-8")
	}
	parts := strings.Split(p, "/")
	if len(parts) > trialfix.MaxDepth {
		return bad("is too deep")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > trialfix.MaxNameBytes {
			return bad("is not a canonical relative path")
		}
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return bad("has a control character or backslash")
		}
	}
	if parts[0] == trialfix.LostFound {
		return bad("is under lost+found, which is not part of a fixture")
	}
	return nil
}

func parseMode(s string) (uint32, error) {
	if !modeRE.MatchString(s) {
		return 0, fmt.Errorf("mode %q is not four octal digits such as 0644", s)
	}
	m, _ := strconv.ParseUint(s, 8, 32)
	return uint32(m), nil
}
