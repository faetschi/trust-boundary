package productsetup

import (
	"bytes"
	"errors"
	"io"
	"strings"
)

// InstallationMarker is compiled into the tbound binary, and ScanInstallationFormat refers to it, so the
// linker keeps it. Update and rollback read a candidate binary's format from its bytes with
// ScanInstallationFormat and never run the candidate. The number is InstallationFormat (a test pins them
// together). A binary without a marker predates it and is LegacyInstallationFormat. A forged marker only
// passes the format check. The self-test and the restoration still guard a binary that does not work.
const InstallationMarker = "tbound-product-installation-format=3;"

// ScanInstallationFormat returns the format a binary declares, read through the one reader (or descriptor) the caller
// validated and bounded by maxBinaryBytes. The first well-formed marker wins. None means the legacy format.
func ScanInstallationFormat(candidate io.ReadSeeker) (int, error) {
	if _, err := candidate.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	// A slice of the one embedded marker, so the binary holds no second copy of the text it searches for.
	pattern := []byte(InstallationMarker[:strings.IndexByte(InstallationMarker, '=')+1])
	const chunk, tail = 1 << 20, 5 // tail: up to four digits and the terminator
	keep := len(pattern) + tail - 1
	buf := make([]byte, 0, keep+chunk)
	reader := io.LimitReader(candidate, maxBinaryBytes+1)
	var total int64
	for {
		n, err := io.ReadFull(reader, buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if total += int64(n); total > maxBinaryBytes {
			return 0, refuse("the binary is too large to be the tbound binary")
		}
		for from := 0; ; {
			i := bytes.Index(buf[from:], pattern)
			if i < 0 {
				break
			}
			if format, ok := formatAfter(buf[from+i+len(pattern):]); ok {
				return format, nil
			}
			from += i + 1
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return LegacyInstallationFormat, nil
		} else if err != nil {
			return 0, err
		}
		// A marker cut by the end of this chunk starts inside the last keep bytes, which the next pass sees whole.
		buf = append(buf[:0], buf[len(buf)-keep:]...)
	}
}

// formatAfter parses the `DIGITS;` that follows the marker's text: one to four digits, a positive number.
func formatAfter(rest []byte) (int, bool) {
	format := 0
	for i, c := range rest[:min(len(rest), 5)] {
		switch {
		case c >= '0' && c <= '9' && i < 4:
			format = format*10 + int(c-'0')
		case c == ';' && format > 0:
			return format, true
		default:
			return 0, false
		}
	}
	return 0, false
}
