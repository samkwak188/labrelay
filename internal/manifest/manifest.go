package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxFile     int64 = 2 << 30
	MaxDataset  int64 = 5 << 30
	MaxManifest       = 2 << 20
	MaxFiles          = 5000
	PatchSize   int64 = 8 << 20
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type File struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Schema     int      `json:"schema_version"`
	Name       string   `json:"name"`
	Source     string   `json:"source_type"`
	Files      []File   `json:"files"`
	Provenance []string `json:"provenance,omitempty"`
}

func NameOK(s string) bool { return namePattern.MatchString(s) }
func PathOK(s string) bool {
	return len(s) <= 1024 && fs.ValidPath(s) && s != "." && !strings.ContainsAny(s, "\\\x00:")
}
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (m Manifest) Size() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}
func (m Manifest) Validate() error {
	if m.Schema != 1 || !NameOK(m.Name) || (m.Source != "generic" && m.Source != "cats") {
		return errors.New("invalid manifest version, name or source_type")
	}
	if len(m.Files) == 0 || len(m.Files) > MaxFiles {
		return errors.New("dataset must contain 1..5000 files")
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range m.Files {
		if !PathOK(f.Path) || seen[f.Path] || f.Size < 0 || f.Size > MaxFile || !hashPattern.MatchString(f.SHA256) || f.Role == "" || len(f.Role) > 64 {
			return fmt.Errorf("invalid or duplicate file: %q", f.Path)
		}
		seen[f.Path] = true
		total += f.Size
		if total > MaxDataset {
			return errors.New("dataset exceeds 5 GiB")
		}
	}
	for p := range seen {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("file/directory collision: %s", parent)
			}
		}
	}
	for _, p := range m.Provenance {
		if !seen[p] {
			return fmt.Errorf("provenance artifact missing: %s", p)
		}
	}
	return nil
}
func (m Manifest) Bytes() ([]byte, error) {
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	sort.Strings(m.Provenance)
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	if len(b) > MaxManifest {
		return nil, errors.New("manifest exceeds 2 MiB")
	}
	return b, nil
}
func Parse(b []byte) (Manifest, error) {
	var m Manifest
	if len(b) > MaxManifest {
		return m, errors.New("manifest exceeds 2 MiB")
	}
	// Reject duplicate keys: JSON last-key-wins behavior must not hide malicious fields.
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(b))); err != nil {
		return m, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, errors.New("trailing JSON")
	}
	return m, m.Validate()
}
func uniqueKeys(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("duplicate JSON key")
			}
			seen[s] = true
			if e = uniqueKeys(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = uniqueKeys(d); e != nil {
				return e
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, e = d.Token()
	return e
}
