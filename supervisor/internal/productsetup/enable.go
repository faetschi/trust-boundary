package productsetup

import (
	"errors"
	"os"
)

// unitEnabled reports whether the unit starts at boot: the multi-user.target.wants link `systemctl enable`
// makes points at the unit file. The link is read the same way on the live host and on a test root.
func (h *host) unitEnabled() bool {
	target := ""
	err := h.withPath(enableLinkPath, false, func(path string) (err error) {
		target, err = os.Readlink(path)
		return err
	})
	return err == nil && target == UnitPath
}

// planEnable plans the boot activation. `systemctl enable` is idempotent, but a rerun on an enabled unit
// must report unchanged and issue no command. A test root only records the command, so it also makes the
// link systemd would make, which keeps a rerun and the doctor truthful there.
func (h *host) planEnable() planned {
	detail := "WantedBy=" + unitWantedBy
	if h.unitEnabled() {
		return planned{Step{"enable", UnitName, "unchanged", detail}, nil}
	}
	return planned{Step{"enable", UnitName, "created", detail}, func() error {
		if err := h.command("systemctl", "enable", UnitName); err != nil {
			return err
		}
		if h.env.Live || !h.writes() {
			return nil
		}
		return h.withPath(enableLinkPath, true, func(path string) error {
			if err := os.Symlink(UnitPath, path); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			return nil
		})
	}}
}
