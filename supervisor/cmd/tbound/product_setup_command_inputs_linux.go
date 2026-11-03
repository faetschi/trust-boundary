//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

func validateProductSetupCommandInputs(in productsetup.HostInputs) error {
	// Even a bundle-less candidate is checked, so allowing Bash cannot bypass the static verifier.
	if in.CommandBundlePath == "" {
		for _, id := range slices.Sorted(maps.Keys(in.PolicyDigests)) {
			if _, _, err := loadProductGatePolicy(id, in.PolicyDigests[id], in.GatePolicyPaths[id], in.Owners, false); err != nil {
				return fmt.Errorf("input policy %s: %w", id, err)
			}
		}
		return nil
	}
	if !bytes.Equal(in.ContainersConfig, productsetup.RenderProductContainersConf()) || !bytes.Equal(in.StorageConfig, productsetup.RenderProductStorageConf()) {
		return errors.New("command configs differ from the fixed execution configs")
	}
	var cfg productServiceConfig
	if err := unmarshalGovernedJSON(in.ServiceConfig, &cfg); err != nil {
		return err
	}
	key, err := decodeProductPublicKey(in.PublicKey)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(in.RegistrationSignature)))
	if err != nil {
		return err
	}
	registration, err := productsession.VerifyRegistration(in.RegistrationData, sig, ed25519.PublicKey(key))
	if err != nil {
		return err
	}
	files := in.CommandFiles
	_, _, err = validateProductCommandInputs(cfg, registration, productCommandInputs{
		Registration: in.RegistrationData, RegistrationSignature: in.RegistrationSignature, PublicKey: in.PublicKey,
		Unit: in.Unit, Profile: files[productsetup.CommandHostProfilePath], ProfileSignature: files[productsetup.CommandHostProfileSignaturePath],
		Template: files[productsetup.CommandCellTemplatePath], Seccomp: files[productsetup.CommandSeccompPath], CosignPublicKey: files[productsetup.CommandCosignPublicKeyPath],
	}, in.Root)
	return err
}
