package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxTestJSONBytes bounds one `go test -json` file.
const maxTestJSONBytes = 1 << 30

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// readRegularFile reads a regular file of at most max bytes.
func readRegularFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, max)
	}
	return data, nil
}

// parseTestJSONFile streams a `go test -json` file into o and returns the sha256 of its bytes.
func parseTestJSONFile(path string, o *testOutcomes) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("test json: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("test json: %s is not a regular file", path)
	}
	h := sha256.New()
	if err := o.addReader(io.TeeReader(io.LimitReader(f, maxTestJSONBytes+1), h)); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	// The parser stops at the end of the stream, so a file over the limit is detected by one more byte.
	if n, _ := io.Copy(io.Discard, io.TeeReader(f, h)); n > 0 {
		return "", fmt.Errorf("test json: %s exceeds %d bytes", path, int64(maxTestJSONBytes))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// digestNonEmptyFile returns the sha256 of a regular, non-empty file.
func digestNonEmptyFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("%s is empty", path)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// writeNewFile creates path with data and refuses an existing file: a pinned input is never rewritten.
func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s exists: remove it deliberately to regenerate", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
