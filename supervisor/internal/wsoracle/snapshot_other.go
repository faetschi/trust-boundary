//go:build !linux

package wsoracle

// Snapshot is unavailable off Linux. It returns an incomplete manifest with an
// unsupported_platform gap, so every evaluation over it is evidence_lost.
func Snapshot(root, label string) Manifest {
	return Manifest{
		Label:    label,
		Complete: false,
		Gaps:     []Gap{{Manifest: label, Kind: GapUnsupportedPlatform}},
	}
}
