package corpusadmit

import (
	"encoding/base64"
	"strings"
)

// CDHRole qualifies the admission by the exact model in the pinned manifest.
func CDHRole(model, role string) string {
	return "cdh-" + base64.RawURLEncoding.EncodeToString([]byte(model)) + "." + role
}
func CDHRoleParts(role string) (string, string, bool) {
	if !strings.HasPrefix(role, "cdh-") {
		return "", "", false
	}
	encoded, base, ok := strings.Cut(strings.TrimPrefix(role, "cdh-"), ".")
	if !ok || (base != "compliant" && base != "trajectory") {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !BoundedIdentity(string(raw), TaskIDMaxBytes) || strings.ContainsAny(string(raw), "|\r\n") || CDHRole(string(raw), base) != role {
		return "", "", false
	}
	return string(raw), base, true
}
