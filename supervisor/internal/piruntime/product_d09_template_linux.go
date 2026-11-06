//go:build linux

package piruntime

import (
	"bytes"
	"encoding/json"
	"fmt"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsetup"
)

// DecodeProductD09CellTemplate is the inverse of DecodeD09CellTemplate. It requires the product registration digest,
// refuses the managed-live-root binding and route probes, and pins the cosign key, OCI layout, and seccomp profile to the
// product's fixed paths. A raw decode never registers the template; only a verified loader does.
func DecodeProductD09CellTemplate(raw []byte) (D09CellTemplate, error) {
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return D09CellTemplate{}, fmt.Errorf("%w: product template is not strict JSON: %v", ErrD09TemplateInvalid, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var template D09CellTemplate
	if err := decoder.Decode(&template); err != nil {
		return D09CellTemplate{}, fmt.Errorf("%w: decode product D09 cell template: %v", ErrD09TemplateInvalid, err)
	}
	if err := validateD09CellMeasurements(template); err != nil {
		return D09CellTemplate{}, err
	}
	if !validDigest(template.ProductRegistrationDigest) || template.ManagedLiveRootRegistrationDigest != "" || template.RouteProbe != nil ||
		template.CosignPublicKeyPath != productsetup.CommandCosignPublicKeyPath || template.OCILayoutPath != productsetup.CommandOCILayoutPath ||
		template.SeccompProfilePath != productsetup.CommandSeccompPath {
		return D09CellTemplate{}, fmt.Errorf("%w: product template is not bound to the product registration and fixed product paths", ErrD09TemplateInvalid)
	}
	return template, nil
}
