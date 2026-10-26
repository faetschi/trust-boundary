// Package productsetup plans and applies the TBound product host setup, update, rollback, uninstall
// and diagnostics. It holds no authority of its own: it never signs, never reads a provider key, and
// never touches a thesis service path. Every host effect goes through Env, so a test root (Env.Root)
// shows exactly which owners, modes and commands a real run would use.
package productsetup

import (
	"os"
	"path"
	"strings"

	"tbound/supervisor/internal/productsession"
)

// Fixed names and paths of the product installation. None equals or sits inside a thesis path.
const (
	ServiceUser  = "tbound-product"
	ServiceGroup = "tbound-product"
	UnitName     = "tbound-product.service"

	ConfigDir         = "/etc/tbound-product"
	CredentialsDir    = ConfigDir + "/credentials"
	ServiceConfigPath = ConfigDir + "/service.json"
	SetupConfigPath   = ConfigDir + "/setup.json"
	StateDir          = "/var/lib/tbound-product"
	TurnsDir          = StateDir + "/turns"
	ProjectsDir       = StateDir + "/projects"
	BackupsDir        = StateDir + "/backups"
	RuntimeDir        = "/run/tbound-product"
	UnitPath          = "/etc/systemd/system/" + UnitName
	BinaryDir         = "/usr/local/lib/tbound-product"
	BinaryPath        = BinaryDir + "/tbound"
	LastKnownGoodPath = BinaryPath + ".last-known-good"
	stagedBinaryPath  = BinaryPath + ".new"
)

// Paths inherited from the product defaults, so setup and the service cannot disagree.
const (
	RegistrationPath          = productsession.DefaultRegistrationPath
	RegistrationSignaturePath = productsession.DefaultRegistrationSignaturePath
	RegistrationPublicKeyPath = productsession.DefaultRegistrationPublicKeyPath
	CatalogPath               = productsession.DefaultCatalogPath
	SocketPath                = productsession.DefaultServiceSocketPath
)

// ThesisRoots are the thesis service's paths. Setup refuses a path equal to or inside one, compared by
// whole path component. cmd/tbound's service config validator keeps the same list, and a test there
// keeps the two equal.
var ThesisRoots = []string{"/etc/tbound", "/var/lib/tbound", "/run/tbound", "/run/tbound-pi"}

// PathInside reports whether p equals root or sits below it, comparing whole components.
func PathInside(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// IsThesisPath reports whether p is a thesis service path.
func IsThesisPath(p string) bool {
	p = path.Clean(p)
	for _, root := range ThesisRoots {
		if PathInside(p, root) {
			return true
		}
	}
	return false
}

type owner int

const (
	ownRoot owner = iota
	ownService
)

func (o owner) String() string {
	if o == ownService {
		return ServiceUser + ":" + ServiceGroup
	}
	return "root:root"
}

// node is one path setup creates and the doctor inspects.
type node struct {
	Path  string
	Dir   bool
	Owner owner
	Mode  os.FileMode
}

// directories lists the directories parents first, with the contract's owners and modes, except that
// the state tree is 0700, not the contract table's 0750: audit.Open refuses a journal whose parent
// directory is not exactly 0700, and the catalog journal lives directly in StateDir. RuntimeDir is
// created by systemd from the unit (RuntimeDirectory=, RuntimeDirectoryMode=) and is not listed.
var directories = []node{
	{ConfigDir, true, ownRoot, 0o755},
	{CredentialsDir, true, ownService, 0o700},
	{StateDir, true, ownService, 0o700},
	{TurnsDir, true, ownService, 0o700},
	{ProjectsDir, true, ownService, 0o700},
	{BackupsDir, true, ownService, 0o700},
	{BinaryDir, true, ownRoot, 0o755},
}

// installedFiles lists the files setup installs. Each has exactly one source of content.
var installedFiles = []node{
	{RegistrationPath, false, ownRoot, 0o644},
	{RegistrationSignaturePath, false, ownRoot, 0o644},
	{RegistrationPublicKeyPath, false, ownRoot, 0o644},
	{ServiceConfigPath, false, ownRoot, 0o644},
	{SetupConfigPath, false, ownRoot, 0o644},
	{UnitPath, false, ownRoot, 0o644},
	{BinaryPath, false, ownRoot, 0o755},
}
