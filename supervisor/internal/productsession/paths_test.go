package productsession

import (
	"path"
	"strings"
	"testing"
)

func insidePath(p, root string) bool {
	pc := strings.Split(strings.Trim(path.Clean(p), "/"), "/")
	rc := strings.Split(strings.Trim(path.Clean(root), "/"), "/")
	if len(pc) < len(rc) {
		return false
	}
	for i := range rc {
		if pc[i] != rc[i] {
			return false
		}
	}
	return true
}

func TestProductDefaultsAvoidThesisPaths(t *testing.T) {
	thesis := []string{"/etc/tbound", "/var/lib/tbound", "/run/tbound", "/run/tbound-pi"}
	if !insidePath("/etc/tbound/x", "/etc/tbound") || insidePath("/etc/tbound-product/x", "/etc/tbound") {
		t.Fatal("component comparison helper is wrong")
	}
	defaults := []string{
		DefaultRegistrationPath, DefaultRegistrationSignaturePath, DefaultRegistrationPublicKeyPath,
		DefaultCatalogPath, DefaultTurnStateRoot, DefaultServiceSocketPath,
	}
	for _, d := range defaults {
		for _, root := range thesis {
			if insidePath(d, root) {
				t.Errorf("product default %s is equal to or inside thesis path %s", d, root)
			}
		}
	}
}
