package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Selection includes the original metadata as opaque artifacts. Empty SHA means
// compute it from the copied snapshot; declared hashes are never silently replaced.
type Selection struct {
	Name, Source string
	Files        []File
	Provenance   []string
}

func catsPath(p string) string {
	if strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00:") {
		return ""
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return ""
		}
	}
	p = path.Clean(p)
	if !PathOK(p) {
		return ""
	}
	return p
}

func readJSON(root *os.Root, p string, v any) error {
	if !PathOK(p) {
		return fmt.Errorf("invalid metadata path %q", p)
	}
	f, e := root.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, MaxManifest+1))
	if e != nil {
		return e
	}
	if len(b) > MaxManifest {
		return fmt.Errorf("metadata too large")
	}
	return json.Unmarshal(b, v)
}
func CATS(rootPath, scenePath, framesPath string) (Selection, error) {
	s := Selection{Source: "cats"}
	root, e := os.OpenRoot(rootPath)
	if e != nil {
		return s, e
	}
	defer root.Close()
	var scene struct {
		ID    string `json:"scene_id"`
		Video string `json:"storage_path"`
		Hash  string `json:"checksum"`
	}
	var fm struct {
		Video  string `json:"source_video"`
		CSV    string `json:"manifest_csv"`
		Count  int    `json:"saved_frame_count"`
		Frames []struct {
			Path  string `json:"path"`
			Index int    `json:"saved_index"`
		} `json:"frames"`
		Files []struct {
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
			Size   int64  `json:"size_bytes"`
			Hash   string `json:"sha256"`
		} `json:"frame_files"`
		Provenance struct {
			Inputs []struct {
				Path string `json:"path"`
				Size int64  `json:"size_bytes"`
				Hash string `json:"sha256"`
			} `json:"inputs"`
		} `json:"provenance"`
	}
	if e = readJSON(root, scenePath, &scene); e != nil {
		return s, e
	}
	if e = readJSON(root, framesPath, &fm); e != nil {
		return s, e
	}
	scene.Video = catsPath(scene.Video)
	fm.Video = catsPath(fm.Video)
	fm.CSV = catsPath(fm.CSV)
	if scene.Video != fm.Video || !PathOK(scene.Video) || !NameOK(scene.ID) {
		return s, fmt.Errorf("CATS video references or scene_id disagree")
	}
	hash := strings.TrimPrefix(scene.Hash, "sha256:")
	if !hashPattern.MatchString(hash) {
		return s, fmt.Errorf("CATS source SHA-256 missing")
	}
	size := int64(-1)
	for _, i := range fm.Provenance.Inputs {
		if catsPath(i.Path) == scene.Video {
			if size >= 0 || i.Hash != hash {
				return s, fmt.Errorf("CATS video provenance mismatch")
			}
			size = i.Size
		}
	}
	if size < 0 {
		return s, fmt.Errorf("CATS source video provenance missing")
	}
	s.Name = scene.ID
	s.Provenance = []string{scenePath, framesPath, fm.CSV}
	s.Files = []File{{Path: scene.Video, Role: "source_video", Size: size, SHA256: hash}}
	for _, p := range s.Provenance {
		if !PathOK(p) {
			return s, fmt.Errorf("invalid CATS metadata path")
		}
		s.Files = append(s.Files, File{Path: p, Role: "provenance", Size: -1})
	}
	if fm.Count < 1 || fm.Count != len(fm.Frames) || fm.Count != len(fm.Files) {
		return s, fmt.Errorf("CATS frame counts disagree")
	}
	paths := map[string]bool{}
	indexes := map[int]bool{}
	for _, f := range fm.Frames {
		f.Path = catsPath(f.Path)
		if !PathOK(f.Path) || paths[f.Path] || f.Index < 0 || f.Index >= fm.Count || indexes[f.Index] {
			return s, fmt.Errorf("invalid CATS frame path/index")
		}
		paths[f.Path] = true
		indexes[f.Index] = true
	}
	for _, f := range fm.Files {
		f.Path = catsPath(f.Path)
		if !paths[f.Path] || !f.Exists || f.Size < 0 || !hashPattern.MatchString(f.Hash) {
			return s, fmt.Errorf("invalid CATS frame record %s", f.Path)
		}
		delete(paths, f.Path)
		s.Files = append(s.Files, File{Path: f.Path, Role: "frame", Size: f.Size, SHA256: f.Hash})
	}
	if len(paths) != 0 {
		return s, fmt.Errorf("CATS frame sets disagree")
	}
	return s, nil
}
