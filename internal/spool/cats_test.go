//go:build linux

package spool

import (
	"context"
	"encoding/json"
	"github.com/samkwak188/labrelay/internal/manifest"
	"os"
	"path/filepath"
	"testing"
)

func TestCATSBundle(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	os.Mkdir(src, 0700)
	write := func(p string, b []byte) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(src, p), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	video := []byte("original footage")
	frame := []byte("frame bytes")
	write("video.mp4", video)
	write("frame.png", frame)
	write("frames.csv", []byte("saved_index,path\n0,frame.png\n"))
	scene := map[string]any{"scene_id": "cats-scene", "storage_path": "video.mp4", "checksum": "sha256:" + manifest.Digest(video), "limitations": []string{"not calibration evidence"}, "license": "preserve this metadata"}
	sb, _ := json.Marshal(scene)
	write("scene.json", sb)
	fm := map[string]any{"source_video": "video.mp4", "manifest_csv": "frames.csv", "saved_frame_count": 1, "frames": []any{map[string]any{"path": "frame.png", "saved_index": 0}}, "frame_files": []any{map[string]any{"path": "frame.png", "exists": true, "size_bytes": len(frame), "sha256": manifest.Digest(frame)}}, "provenance": map[string]any{"cwd": "/historical/nonexistent/path", "argv": []string{"do-not-execute"}, "inputs": []any{map[string]any{"path": "video.mp4", "size_bytes": len(video), "sha256": manifest.Digest(video)}}}}
	fb, _ := json.Marshal(fm)
	write("frames.json", fb)
	sel, e := manifest.CATS(src, "scene.json", "frames.json")
	if e != nil {
		t.Fatal(e)
	}
	if len(sel.Files) != 5 {
		t.Fatal(sel)
	}
	s, e := Open(filepath.Join(dir, "spool"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	id, _, e := s.Prepare(context.Background(), src, sel, false)
	if e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(filepath.Join(s.Dir, "snapshots", id, "files", "scene.json"))
	if string(got) != string(sb) {
		t.Fatal("provenance changed")
	}
	write("frame.png", []byte("wrong bytes"))
	if _, _, e = s.Prepare(context.Background(), src, sel, false); e == nil {
		t.Fatal("corrupt frame accepted")
	}
	os.Remove(filepath.Join(src, "video.mp4"))
	if _, _, e = s.Prepare(context.Background(), src, sel, false); e == nil {
		t.Fatal("missing footage accepted")
	}
	scene["storage_path"] = "other.mp4"
	sb, _ = json.Marshal(scene)
	write("scene.json", sb)
	if _, e = manifest.CATS(src, "scene.json", "frames.json"); e == nil {
		t.Fatal("mismatched video references accepted")
	}
}

func TestPinnedCATSMetadata(t *testing.T) {
	root := "../../testdata/cats"
	sel, e := manifest.CATS(root, "data/scenes/chiangmai_intersection.json", "data/frames/chiangmai_intersection/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	if len(sel.Files) != 33 || sel.Name != "chiangmai_intersection" {
		t.Fatalf("unexpected upstream selection: %+v", sel)
	}
	s, e := Open(filepath.Join(t.TempDir(), "spool"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, _, e = s.Prepare(context.Background(), root, sel, false); e == nil {
		t.Fatal("metadata-only checkout accepted without original video")
	}
}
