//go:build linux

package productprovider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/providerbridge"
)

var (
	ErrCredential      = errors.New("credential absent or insecure: require service-owned directory 0700, regular key file 0600, protected ancestors and one printable API key")
	ErrExists          = errors.New("credential already exists; use --replace or rotate")
	ErrAbsent          = errors.New("credential is absent; use setup")
	ErrStorage         = errors.New("credential storage operation failed; check administrative permissions and available disk space")
	ErrServiceIdentity = errors.New("provider credentials must be read as their owner; run the live check as the product service account")
)

const maxKeyBytes = 4096

// Store has no environment-derived paths. The unexported override exists only
// for in-package tests on a normal-user Linux filesystem.
type Store struct{ directory, exemptThrough string }

func NewStore() *Store { return &Store{directory: CredentialDirectory} }

type Status struct {
	Present     bool   `json:"present"`
	UID         uint32 `json:"uid"`
	GID         uint32 `json:"gid"`
	Mode        string `json:"mode"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
type Rotation struct{ OldFingerprint, NewFingerprint string }

func Fingerprint(key []byte) string {
	sum := sha256.Sum256(append([]byte("tbound-product-provider-credential/v1\n"), []byte(strings.TrimSpace(string(key)))...))
	return hex.EncodeToString(sum[:4])
}

func normalizedKey(key []byte) ([]byte, error) {
	if _, err := openrouter.CredentialsFromKey(key); err != nil {
		return nil, ErrCredential
	}
	return []byte(strings.TrimSpace(string(key))), nil
}

// ReadSource requires protected root/caller-owned ancestors without symlinks,
// then a caller-owned regular file, 0400/0600 and bounded. No environment lookup occurs.
func ReadSource(path string) ([]byte, error) {
	key, err := openrouter.ReadCredentialSourceFile(path)
	if err != nil {
		return nil, ErrCredential
	}
	return normalizedKey(key)
}

// directoryFD pins each ancestor before opening the next. Higher ancestors
// follow the existing root-owned, non-writable service-reader rule. The last
// directory is service-owned and exactly 0700. No symlink is ever followed.
func (s *Store) directoryFD(create bool, uid, gid int) (int, error) {
	path := s.directory
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return -1, ErrCredential
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrCredential
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	walked := ""
	for i := -1; i < len(components); i++ {
		last := i == len(components)-1
		if i >= 0 {
			walked += "/" + components[i]
			next, openErr := unix.Openat(fd, components[i], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if errors.Is(openErr, unix.ENOENT) && create {
				mode := uint32(0755)
				if last {
					mode = 0700
				}
				if err = unix.Mkdirat(fd, components[i], mode); err == nil {
					next, openErr = unix.Openat(fd, components[i], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
					if openErr == nil {
						owner := 0
						group := 0
						if last {
							owner, group = uid, gid
						}
						err = unix.Fchown(next, owner, group)
						if err == nil {
							err = unix.Fchmod(next, mode)
						}
						if err == nil {
							err = unix.Fsync(next)
						}
						if err == nil {
							err = unix.Fsync(fd)
						}
						if err != nil {
							unix.Close(next)
							unix.Close(fd)
							return -1, ErrStorage
						}
					}
				} else {
					openErr = err
				}
			}
			unix.Close(fd)
			if openErr != nil {
				if errors.Is(openErr, unix.ENOENT) {
					return -1, ErrAbsent
				}
				return -1, ErrCredential
			}
			fd = next
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		exempt := s.exemptThrough != "" && (walked == "" || walked == s.exemptThrough || strings.HasPrefix(s.exemptThrough, walked+"/"))
		valid := st.Mode&unix.S_IFMT == unix.S_IFDIR
		if last {
			valid = valid && st.Mode&07777 == 0700 && (uid < 0 || st.Uid == uint32(uid)) && (gid < 0 || st.Gid == uint32(gid))
		} else if !exempt {
			valid = valid && st.Uid == 0 && st.Mode&0022 == 0
		}
		if err != nil || !valid {
			unix.Close(fd)
			return -1, ErrCredential
		}
	}
	return fd, nil
}

func readKeyAt(dir int, name string) ([]byte, Status, error) {
	var parent unix.Stat_t
	if unix.Fstat(dir, &parent) != nil {
		return nil, Status{}, ErrCredential
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, Status{}, nil
	}
	if err != nil {
		return nil, Status{}, ErrCredential
	}
	f := os.NewFile(uintptr(fd), "product-provider-key")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != parent.Uid || st.Gid != parent.Gid {
		return nil, Status{}, ErrCredential
	}
	key, err := io.ReadAll(io.LimitReader(f, maxKeyBytes+1))
	if err != nil || len(key) > maxKeyBytes {
		return nil, Status{}, ErrCredential
	}
	key, err = normalizedKey(key)
	if err != nil {
		return nil, Status{}, err
	}
	return key, Status{Present: true, UID: st.Uid, GID: st.Gid, Mode: "0600", Fingerprint: Fingerprint(key)}, nil
}

func profileKeyName(id string) (string, error) {
	if _, ok := Lookup(id); !ok {
		return "", errors.New("unknown product provider profile")
	}
	return id + ".key", nil
}

func (s *Store) Status(id string) (Status, error) {
	name, err := profileKeyName(id)
	if err != nil {
		return Status{}, err
	}
	fd, err := s.directoryFD(false, -1, -1)
	if errors.Is(err, ErrAbsent) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	defer unix.Close(fd)
	_, status, err := readKeyAt(fd, name)
	return status, err
}

// Install serializes writers on the pinned directory, syncs a same-directory
// temporary file, then atomically renames and syncs the directory. NOREPLACE
// also closes the race with writers that do not take the advisory lock.
func (s *Store) Install(id string, uid, gid int, key []byte, replace, requireExisting bool) (Rotation, error) {
	name, err := profileKeyName(id)
	if err != nil {
		return Rotation{}, err
	}
	key, err = normalizedKey(key)
	if err != nil {
		return Rotation{}, err
	}
	if uid < 0 || gid < 0 {
		return Rotation{}, ErrCredential
	}
	fd, err := s.directoryFD(true, uid, gid)
	if err != nil {
		return Rotation{}, err
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX) != nil {
		return Rotation{}, ErrStorage
	}
	_, old, err := readKeyAt(fd, name)
	if err != nil {
		return Rotation{}, err
	}
	if old.Present && !replace {
		return Rotation{}, ErrExists
	}
	if !old.Present && requireExisting {
		return Rotation{}, ErrAbsent
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Rotation{}, ErrStorage
	}
	temp := ".key-" + hex.EncodeToString(nonce[:])
	keyFD, err := unix.Openat(fd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return Rotation{}, ErrStorage
	}
	defer unix.Unlinkat(fd, temp, 0)
	f := os.NewFile(uintptr(keyFD), "product-provider-key")
	err = f.Chown(uid, gid)
	if err == nil {
		err = f.Chmod(0600)
	}
	if err == nil {
		_, err = f.Write(key)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return Rotation{}, ErrStorage
	}
	if replace {
		err = unix.Renameat(fd, temp, fd, name)
	} else {
		err = unix.Renameat2(fd, temp, fd, name, unix.RENAME_NOREPLACE)
	}
	if errors.Is(err, unix.EEXIST) {
		return Rotation{}, ErrExists
	}
	if err != nil || unix.Fsync(fd) != nil {
		return Rotation{}, ErrStorage
	}
	return Rotation{OldFingerprint: old.Fingerprint, NewFingerprint: Fingerprint(key)}, nil
}

func (s *Store) Remove(id string) error {
	name, err := profileKeyName(id)
	if err != nil {
		return err
	}
	fd, err := s.directoryFD(false, -1, -1)
	if errors.Is(err, ErrAbsent) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX) != nil {
		return ErrStorage
	}
	_, status, err := readKeyAt(fd, name)
	if err != nil {
		return err
	}
	if !status.Present {
		return nil
	}
	if unix.Unlinkat(fd, name, 0) != nil || unix.Fsync(fd) != nil {
		return ErrStorage
	}
	return nil
}

func (s *Store) credentials(id string) (openrouter.Credentials, error) {
	name, err := profileKeyName(id)
	if err != nil {
		return openrouter.Credentials{}, err
	}
	fd, err := s.directoryFD(false, -1, -1)
	if err != nil {
		return openrouter.Credentials{}, ErrCredential
	}
	defer unix.Close(fd)
	key, status, err := readKeyAt(fd, name)
	if err != nil || !status.Present {
		return openrouter.Credentials{}, ErrCredential
	}
	if status.UID != uint32(os.Geteuid()) {
		return openrouter.Credentials{}, ErrServiceIdentity
	}
	return openrouter.CredentialsFromKey(key)
}

// ConversationFactory is the service integration point. The path must name
// this profile's key, with the same strict ancestor and ownership contract.
func ConversationFactory(id, credentialPath string) (func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error), error) {
	name, err := profileKeyName(id)
	if err != nil || filepath.Base(credentialPath) != name || !filepath.IsAbs(credentialPath) || filepath.Clean(credentialPath) != credentialPath {
		return nil, ErrCredential
	}
	s := &Store{directory: filepath.Dir(credentialPath)}
	return s.conversationFactory(id), nil
}

func (s *Store) conversationFactory(id string) func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error) {
	return func(cfg providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		if cfg.ProfileID != id || (cfg.Route != (openrouter.Route{}) && cfg.Route != openrouter.EngineeringRoute()) || cfg.Spend != nil {
			return nil, errors.New("conversation does not match the product provider profile; monetary ceilings unsupported")
		}
		credentials, err := s.credentials(id)
		if err != nil {
			return nil, err
		}
		cfg.Route = openrouter.EngineeringRoute()
		selectLimit := cfg.SelectCompletionTokenLimit
		cfg.SelectCompletionTokenLimit = func(ctx context.Context) (uint64, error) {
			if selectLimit == nil {
				return 0, errors.New("product provider requires a host completion-token ceiling")
			}
			n, err := selectLimit(ctx)
			p, _ := Lookup(id)
			if err != nil || n < p.MinCompletionTokens || n > p.MaxCompletionTokens {
				return 0, errors.New("completion-token ceiling outside product profile bounds")
			}
			return n, nil
		}
		conversation, err := providerbridge.NewFromCredentials(cfg, credentials, journal)
		if err != nil {
			return nil, errors.New(credentials.Redact(err.Error()))
		}
		return conversation, nil
	}
}
