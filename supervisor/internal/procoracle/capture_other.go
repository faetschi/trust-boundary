//go:build !linux

package procoracle

// Capture is unavailable off Linux. It returns a snapshot with an
// unsupported_platform gap, so every evaluation over it is evidence_lost.
func Capture(cell CellReader, view HostView, p CaptureParams) Snapshot {
	return Snapshot{
		Seq: p.Seq, Point: p.Point, RegistrationDigest: p.Registration.Digest(), Registry: p.Registry,
		Gaps: []Gap{{Domain: DomainCell, Code: "unsupported_platform"}},
	}
}
