package projectbundle

// ChangeOp is the operation an export applies to one path.
type ChangeOp string

const (
	OpCreate ChangeOp = "create"
	OpModify ChangeOp = "modify"
	OpDelete ChangeOp = "delete"
	// OpChmod toggles the executable bit of a file whose content is unchanged.
	OpChmod ChangeOp = "chmod"
)

// Change is one path of an export. Create, modify and chmod carry the target state of the path.
// Delete carries only the type of what is removed. A create or modify of a file is followed by its
// content in the stream; nothing else is. A path never changes type: that is not supported.
type Change struct {
	Path       string    `json:"path"`
	Op         ChangeOp  `json:"op"`
	Type       EntryType `json:"type"`
	Executable bool      `json:"executable"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
}

// ExportHeader binds the baseline manifest digest the changes start from to the manifest digest of
// the tree they produce. Changes are sorted strictly by path.
type ExportHeader struct {
	Format         string   `json:"format"`
	BaselineDigest string   `json:"baseline_digest"`
	TargetDigest   string   `json:"target_digest"`
	Changes        []Change `json:"changes"`
}

func (c Change) hasContent() bool {
	return c.Type == TypeFile && (c.Op == OpCreate || c.Op == OpModify)
}

func (h ExportHeader) marshal() ([]byte, error) {
	if h.Changes == nil {
		h.Changes = []Change{}
	}
	return canonicalJSON(h)
}

// Digest is "sha256:" plus the hex SHA-256 of the canonical header, which binds every content digest.
func (h ExportHeader) Digest() (string, error) {
	data, err := h.marshal()
	if err != nil {
		return "", err
	}
	return digestOf(data), nil
}

// Validate checks the header against the format rules and limits, without touching any file.
func (h ExportHeader) Validate(l Limits) error {
	if err := l.validate(); err != nil {
		return err
	}
	if h.Format != ExportFormat {
		return invalid("export format %q is not %q", h.Format, ExportFormat)
	}
	if !validDigest(h.BaselineDigest) || !validDigest(h.TargetDigest) {
		return invalid("export digests are malformed")
	}
	if len(h.Changes) == 0 || len(h.Changes) > l.MaxEntries {
		return invalid("%d changes are outside 1..%d", len(h.Changes), l.MaxEntries)
	}
	var total int64
	for i, c := range h.Changes {
		if err := validateManifestPath(c.Path); err != nil {
			return invalid("change %d path %q: %v", i, c.Path, err)
		}
		if i > 0 && c.Path <= h.Changes[i-1].Path {
			return invalid("change %q is out of order or duplicated", c.Path)
		}
		if c.Type != TypeFile && c.Type != TypeDir {
			return invalid("change %q has unknown type %q", c.Path, c.Type)
		}
		switch {
		case c.Op == OpDelete || (c.Op == OpCreate && c.Type == TypeDir):
			if c.Executable || c.Size != 0 || c.SHA256 != "" {
				return invalid("change %q carries fields its operation does not use", c.Path)
			}
		case c.Op == OpCreate || c.Op == OpModify || c.Op == OpChmod:
			if c.Type != TypeFile {
				return invalid("change %q: %s applies to files only", c.Path, c.Op)
			}
			if c.Size < 0 || c.Size > l.MaxFileBytes || !validHex(c.SHA256) {
				return invalid("change %q has an invalid size or sha256", c.Path)
			}
			if c.hasContent() {
				if total += c.Size; total > l.MaxTotalBytes {
					return invalid("total content exceeds the limit of %d at %q", l.MaxTotalBytes, c.Path)
				}
			}
		default:
			return invalid("change %q has unknown operation %q", c.Path, c.Op)
		}
	}
	return nil
}
