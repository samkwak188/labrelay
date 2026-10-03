//go:build linux

package spool

import (
	"context"
	"github.com/samkwak188/labrelay/internal/manifest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSnapshotCrash(t *testing.T) {
	if os.Getenv("LABRELAY_CHILD") == "1" {
		s, e := Open(os.Getenv("LABRELAY_TEST_SPOOL"))
		if e != nil {
			t.Fatal(e)
		}
		sel, e := Generic(os.Getenv("LABRELAY_TEST_SOURCE"), "crash")
		if e != nil {
			t.Fatal(e)
		}
		_, _, e = s.Prepare(context.Background(), os.Getenv("LABRELAY_TEST_SOURCE"), sel, false)
		if e != nil {
			t.Fatal(e)
		}
		s.Close()
		return
	}
	for _, point := range []string{"snapshot_file", "snapshot_manifest", "snapshot_rename", "snapshot_journal"} {
		t.Run(point, func(t *testing.T) {
			base := t.TempDir()
			src := filepath.Join(base, "source")
			os.Mkdir(src, 0700)
			os.WriteFile(filepath.Join(src, "a"), []byte("preserve original"), 0600)
			sp := filepath.Join(base, "spool")
			c := exec.Command(os.Args[0], "-test.run=^TestSnapshotCrash$")
			c.Env = append(os.Environ(), "LABRELAY_CHILD=1", "LABRELAY_FAULT="+point, "LABRELAY_TEST_SPOOL="+sp, "LABRELAY_TEST_SOURCE="+src)
			e := c.Run()
			if x, ok := e.(*exec.ExitError); !ok || x.ExitCode() != 86 {
				t.Fatalf("fault didn't terminate process: %v", e)
			}
			s, e := Open(sp)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			bs, e := s.Bundles()
			if e != nil {
				t.Fatal(e)
			}
			want := 0
			if point == "snapshot_rename" || point == "snapshot_journal" {
				want = 1
			}
			if len(bs) != want {
				t.Fatalf("got %d queued, want %d", len(bs), want)
			}
			b, _ := os.ReadFile(filepath.Join(src, "a"))
			if string(b) != "preserve original" {
				t.Fatal("source changed")
			}
		})
	}
}
func TestQuotaSymlinkAndHash(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "source")
	os.Mkdir(src, 0700)
	os.WriteFile(filepath.Join(src, "a"), []byte("hello"), 0600)
	s, e := Open(filepath.Join(base, "spool"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	sel, _ := Generic(src, "sample")
	s.Quota = 1
	if _, _, e = s.Prepare(context.Background(), src, sel, false); e == nil {
		t.Fatal("quota ignored")
	}
	s.Quota = 20 << 30
	sel.Files[0].SHA256 = manifest.Digest([]byte("wrong"))
	if _, _, e = s.Prepare(context.Background(), src, sel, false); e == nil {
		t.Fatal("hash ignored")
	}
	os.Symlink("a", filepath.Join(src, "link"))
	if _, e = Generic(src, "sample"); e == nil {
		t.Fatal("symlink accepted")
	}
}
