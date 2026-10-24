package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/evalscore"
)

func loadCDHBaseline(pin pinnedFile) (*evalscore.Input, error) {
	if !filepath.IsAbs(pin.Path) || !digestRE.MatchString(pin.SHA256) {
		return nil, errors.New("C-DH needs cdh_baseline with an absolute path and pinned sha256")
	}
	f, err := os.Open(pin.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("C-DH baseline is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, evalscore.MaxInputBytes+1))
	if err != nil || len(data) > evalscore.MaxInputBytes {
		return nil, errors.New("C-DH baseline unreadable or too large")
	}
	sum := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(sum[:]) != pin.SHA256 {
		return nil, errors.New("C-DH baseline digest mismatch")
	}
	in, err := evalscore.Load(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if in.CDH != nil {
		return nil, errors.New("C-DH baseline must be static C")
	}
	return &in, nil
}
