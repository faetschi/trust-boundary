package productsetup

import (
	"os"
	"strings"
)

// learnServiceIDs resolves the service account's IDs once, when the account exists.
func (h *host) learnServiceIDs() {
	if h.haveIDs {
		return
	}
	if uid, gid, found, err := h.lookupUser(ServiceUser); err == nil && found {
		h.uid, h.gid, h.haveIDs = uid, gid, true
	}
}

// hostInputs describes the inputs of the service's host profile for the static check. Owners are root and,
// once the account exists, the service account: on a first install only root may own an input. A test root
// also admits the test process, because setup cannot chown there.
func (h *host) hostInputs(cfg Config, summary RegistrationSummary, key []byte, keyPath string) HostInputs {
	policies := make(map[string]string, len(cfg.GatePolicyPaths))
	for id, p := range cfg.GatePolicyPaths {
		policies[id] = h.real(p)
	}
	owners := []uint32{0}
	if h.haveIDs {
		owners = append(owners, uint32(h.uid))
	}
	if h.root != "/" {
		owners = append(owners, uint32(os.Geteuid()))
	}
	return HostInputs{PolicyDigests: summary.PolicyDigests, GatePolicyPaths: policies, GitPath: h.real(cfg.GitPath), GitDigest: cfg.GitDigest,
		XattrCalibrationPath: h.real(cfg.XattrCalibrationPath), PublicKey: key, PublicKeyPath: keyPath, Owners: owners, Registration: summary}
}

// refuseBadHostInputs runs the static host-input check before setup writes anything. It records every
// check in the report and refuses, naming each failed input and its reason. The live scanner checks are
// not part of it: `doctor --self-test` and the service start cover them.
func (h *host) refuseBadHostInputs(cfg Config, summary RegistrationSummary, key []byte, keyPath string) error {
	if h.env.CheckHostInputs == nil {
		return nil
	}
	checks := h.env.CheckHostInputs(h.hostInputs(cfg, summary, key, keyPath))
	h.rep.Checks = append(h.rep.Checks, checks...)
	var failed []string
	for _, c := range checks {
		if c.Status == Fail {
			failed = append(failed, c.Name+": "+c.Detail)
		}
	}
	if len(failed) > 0 {
		return refuse("%s; nothing was changed", strings.Join(failed, "; "))
	}
	return nil
}

// installedHostInputChecks checks the inputs the installed setup record names, for the doctor.
func (h *host) installedHostInputChecks(summary RegistrationSummary) []Check {
	if h.env.CheckHostInputs == nil {
		return nil
	}
	keyPath := h.real(RegistrationPublicKeyPath)
	record, recordErr := readSource(h.real(SetupConfigPath))
	key, keyErr := readSource(keyPath)
	var cfg Config
	if recordErr == nil {
		cfg, recordErr = parseInstalled(record)
	}
	if recordErr != nil || keyErr != nil {
		return []Check{{Name: "host inputs", Status: Fail, Detail: "the installed setup record or registration public key is unreadable", Fix: "run `tbound product setup`"}}
	}
	h.learnServiceIDs()
	return h.env.CheckHostInputs(h.hostInputs(cfg, summary, key, keyPath))
}
