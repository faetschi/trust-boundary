//go:build linux

package sessionrepo

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"tbound/supervisor/internal/delta"
)

// ProductPreflight contains the same typed inputs used by the effect path. It grants no authority.
type ProductPreflight struct {
	Tool     string
	Path     string
	MaxBytes int64
	Content  []byte
	Edits    []Replacement
}

type PreflightRefusal struct {
	ArgumentDigest string `json:"typed_argument_digest"`
	Code           string `json:"code"`
	Hint           string `json:"hint"`
}

// CheckProductPreflight verifies the sealed generation under the Store lock without authorizing,
// reserving, or executing an effect. Historical generations are accepted for inert replay only.
func (s *Store) CheckProductPreflight(generationID, tree string, input ProductPreflight) (*PreflightRefusal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
		return nil, err
	}
	g := s.generationByID(generationID)
	if g == nil || g.snapshot.TreeDigest != tree {
		return nil, ErrGenerationModified
	}
	if err := s.verifyGenerationLocked(g); err != nil {
		return nil, err
	}
	if err := validateRelativePath(input.Path); err != nil {
		return nil, err
	}
	var value any
	switch input.Tool {
	case "read":
		if input.MaxBytes <= 0 || input.MaxBytes > maxReadBytes {
			return nil, ErrOperationOutputLimit
		}
		value = struct {
			Path     string `json:"path"`
			MaxBytes int64  `json:"max_bytes"`
		}{input.Path, input.MaxBytes}
	case "write":
		if int64(len(input.Content)) > s.options.Limits.MaxFileBytes {
			return nil, ErrOperationOutputLimit
		}
		value = struct {
			Path          string `json:"path"`
			ContentDigest string `json:"content_digest"`
			ContentBytes  int    `json:"content_bytes"`
		}{input.Path, digestBytes(input.Content), len(input.Content)}
	case "edit":
		if len(input.Edits) == 0 || len(input.Edits) > maxEditReplacements {
			return nil, ErrInvalidEdit
		}
		for _, e := range input.Edits {
			if e.OldText == "" || !utf8.ValidString(e.OldText) || !utf8.ValidString(e.NewText) {
				return nil, ErrInvalidEdit
			}
		}
		value = struct {
			Path  string        `json:"path"`
			Edits []Replacement `json:"edits"`
		}{input.Path, input.Edits}
	default:
		return nil, ErrInvalidOptions
	}
	digest, err := argumentsDigest(value)
	if err != nil {
		return nil, err
	}
	refuse := func(code, hint string) (*PreflightRefusal, error) { return &PreflightRefusal{digest, code, hint}, nil }
	var target delta.ObjectState
	for _, object := range g.snapshot.Manifest.Objects {
		if object.Path == input.Path {
			target = object.State
			break
		}
	}
	if input.Tool == "read" {
		if !target.Exists {
			// A non-directory ancestor is a resolution error, not a proven missing path.
			for _, object := range g.snapshot.Manifest.Objects {
				if len(object.Path) < len(input.Path) && bytes.HasPrefix([]byte(input.Path), []byte(object.Path+"/")) && object.State.Type != delta.ObjectDirectory {
					return nil, ErrUnsupportedTarget
				}
			}
			return refuse("read_missing", "Check the path and read an existing file.")
		}
		return nil, nil
	}
	if target.Exists && target.Type != delta.ObjectRegular {
		return refuse("target_not_regular", "Choose a regular file path.")
	}
	if input.Tool != "edit" || !target.Exists {
		return nil, nil
	}
	original, _, err := readRegularAt(g.root, input.Path, s.options.Limits.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	if err := s.verifyGenerationLocked(g); err != nil {
		return nil, err
	}
	for _, edit := range input.Edits {
		needle := []byte(edit.OldText)
		first := bytes.Index(original, needle)
		if first < 0 {
			return refuse("edit_old_text_absent", "Read the file and use old text that occurs exactly once.")
		}
		if bytes.Index(original[first+len(needle):], needle) >= 0 {
			return refuse("edit_old_text_ambiguous", "Include more surrounding text so the old text occurs exactly once.")
		}
	}
	// Overlapping replacements remain outside the soft-refusal contract.
	if _, err := applyReplacements(original, input.Edits); err != nil {
		return nil, fmt.Errorf("preflight edit: %w", err)
	}
	return nil, nil
}
