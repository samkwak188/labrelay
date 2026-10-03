//go:build linux

package spool

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/samkwak188/labrelay/internal/fault"
	"github.com/samkwak188/labrelay/internal/manifest"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

type Spool struct {
	Dir   string
	DB    *sql.DB
	Quota int64
}
type Bundle struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	State     string          `json:"state"`
	Ingestion string          `json:"ingestion,omitempty"`
	Error     string          `json:"error,omitempty"`
	Receipt   json.RawMessage `json:"receipt,omitempty"`
	Published int64           `json:"published_at,omitempty"`
}

func Open(dir string) (*Spool, error) {
	dir, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	if e = os.Chmod(dir, 0700); e != nil {
		return nil, e
	}
	var st unix.Statfs_t
	if e = unix.Statfs(dir, &st); e != nil {
		return nil, e
	}
	// WSL shared Windows mounts do not provide the tested Linux durability contract.
	if st.Type == 0x9fa0 || st.Type == 0x5346544e || st.Type == 0x65735546 {
		return nil, fmt.Errorf("spool must be on a Linux filesystem, not a Windows/shared/FUSE mount")
	}
	for _, p := range []string{"snapshots", "tmp"} {
		if e = os.MkdirAll(filepath.Join(dir, p), 0700); e != nil {
			return nil, e
		}
	}
	db, e := sql.Open("sqlite", filepath.Join(dir, "journal.sqlite"))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS bundles(id TEXT PRIMARY KEY,name TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'QUEUED',ingestion TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',receipt BLOB,published INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS transfers(bundle TEXT NOT NULL,path TEXT NOT NULL,url TEXT NOT NULL,offset INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(bundle,path));`)
	if e != nil {
		db.Close()
		return nil, e
	}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE IF NOT EXISTS inventory(bundle TEXT NOT NULL,path TEXT NOT NULL,size_bytes INTEGER NOT NULL,sha256 TEXT NOT NULL,PRIMARY KEY(bundle,path));`)
	if e != nil {
		db.Close()
		return nil, e
	}
	s := &Spool{dir, db, 20 << 30}
	if e = s.Recover(); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Spool) Close() error { return s.DB.Close() }
func (s *Spool) BindTarget(origin, project string) error {
	target := origin + "/" + project
	if _, e := s.DB.Exec(`INSERT OR IGNORE INTO settings(key,value) VALUES('target',?)`, target); e != nil {
		return e
	}
	var existing string
	if e := s.DB.QueryRow(`SELECT value FROM settings WHERE key='target'`).Scan(&existing); e != nil {
		return e
	}
	if existing != target {
		return fmt.Errorf("spool is bound to %s; use a separate LABRELAY_SPOOL for a different server/project", existing)
	}
	return nil
}
func (s *Spool) registerSnapshot(id string, m manifest.Manifest) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`INSERT INTO bundles(id,name) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET state=CASE WHEN state='PRUNED' THEN 'PUBLISHED' ELSE state END`, id, m.Name); e != nil {
		return e
	}
	for _, f := range m.Files {
		if _, e = tx.Exec(`INSERT OR IGNORE INTO inventory(bundle,path,size_bytes,sha256) VALUES(?,?,?,?)`, id, f.Path, f.Size, f.SHA256); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Spool) Lock(name string) (func(), error) {
	f, e := os.OpenFile(filepath.Join(s.Dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("another %s operation is running", name)
	}
	return func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (s *Spool) Recover() error {
	unlock, e := s.Lock("prepare")
	if e != nil {
		return nil
	}
	defer unlock()
	entries, e := os.ReadDir(filepath.Join(s.Dir, "snapshots"))
	if e != nil {
		return e
	}
	for _, d := range entries {
		if !d.IsDir() || len(d.Name()) != 64 {
			continue
		}
		b, e := os.ReadFile(filepath.Join(s.Dir, "snapshots", d.Name(), "manifest.json"))
		if e != nil {
			return e
		}
		m, e := manifest.Parse(b)
		if e != nil || manifest.Digest(b) != d.Name() {
			return fmt.Errorf("invalid snapshot %s", d.Name())
		}
		if e = s.registerSnapshot(d.Name(), m); e != nil {
			return e
		}
	}
	// Only our private temporary snapshots are removed while the preparer lock is held.
	entries, e = os.ReadDir(filepath.Join(s.Dir, "tmp"))
	if e != nil {
		return e
	}
	for _, d := range entries {
		if e = os.RemoveAll(filepath.Join(s.Dir, "tmp", d.Name())); e != nil {
			return e
		}
	}
	return nil
}
func Generic(rootPath, name string) (manifest.Selection, error) {
	s := manifest.Selection{Name: name, Source: "generic"}
	root, e := os.OpenRoot(rootPath)
	if e != nil {
		return s, e
	}
	defer root.Close()
	e = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink prohibited: %s", p)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() || !manifest.PathOK(p) {
			return fmt.Errorf("not a regular safe file: %s", p)
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		s.Files = append(s.Files, manifest.File{Path: p, Role: "data", Size: info.Size()})
		return nil
	})
	return s, e
}
func (s *Spool) Prepare(ctx context.Context, rootPath string, sel manifest.Selection, dry bool) (string, int64, error) {
	if !manifest.NameOK(sel.Name) || len(sel.Files) == 0 || len(sel.Files) > manifest.MaxFiles {
		return "", 0, errors.New("invalid dataset name or file count")
	}
	unlock, e := s.Lock("prepare")
	if e != nil {
		return "", 0, e
	}
	defer unlock()
	rootPath, e = filepath.Abs(rootPath)
	if e != nil {
		return "", 0, e
	}
	if s.Dir == rootPath || strings.HasPrefix(s.Dir, rootPath+string(os.PathSeparator)) {
		return "", 0, errors.New("spool must not be inside source directory")
	}
	root, e := os.OpenRoot(rootPath)
	if e != nil {
		return "", 0, e
	}
	defer root.Close()
	var total int64
	seen := map[string]bool{}
	for i, f := range sel.Files {
		if !manifest.PathOK(f.Path) || seen[f.Path] {
			return "", 0, fmt.Errorf("invalid/duplicate path %q", f.Path)
		}
		seen[f.Path] = true
		// Reject every symlink component, including intermediate directories.
		p := ""
		for _, part := range strings.Split(f.Path, "/") {
			p = filepath.Join(p, part)
			st, e := root.Lstat(p)
			if e != nil {
				return "", 0, fmt.Errorf("required artifact %s: %w", p, e)
			}
			if st.Mode()&os.ModeSymlink != 0 {
				return "", 0, fmt.Errorf("symlink prohibited: %s", p)
			}
		}
		st, e := root.Stat(f.Path)
		if e != nil || !st.Mode().IsRegular() {
			return "", 0, fmt.Errorf("not a regular file: %s", f.Path)
		}
		if f.Size >= 0 && st.Size() != f.Size {
			return "", 0, fmt.Errorf("size mismatch: %s", f.Path)
		}
		sel.Files[i].Size = st.Size()
		total += st.Size()
		if st.Size() > manifest.MaxFile || total > manifest.MaxDataset {
			return "", 0, errors.New("dataset size limit exceeded")
		}
	}
	var used int64
	e = filepath.WalkDir(filepath.Join(s.Dir, "snapshots"), func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type().IsRegular() {
			st, e := d.Info()
			if e != nil {
				return e
			}
			used += st.Size()
		}
		return nil
	})
	if e != nil {
		return "", total, e
	}
	var st unix.Statfs_t
	if e = unix.Statfs(s.Dir, &st); e != nil {
		return "", total, e
	}
	if used+total > s.Quota || uint64(total)+(1<<30) > st.Bavail*uint64(st.Bsize) {
		return "", total, errors.New("insufficient spool quota or free disk; unpublished data will not be evicted")
	}
	if dry {
		return "", total, nil
	}
	tmp, e := os.MkdirTemp(filepath.Join(s.Dir, "tmp"), "prepare-")
	if e != nil {
		return "", total, e
	}
	defer os.RemoveAll(tmp)
	if e = os.Mkdir(filepath.Join(tmp, "files"), 0700); e != nil {
		return "", total, e
	}
	dst, e := os.OpenRoot(filepath.Join(tmp, "files"))
	if e != nil {
		return "", total, e
	}
	defer dst.Close()
	for i, f := range sel.Files {
		if e = ctx.Err(); e != nil {
			return "", total, e
		}
		if e = dst.MkdirAll(filepath.Dir(f.Path), 0700); e != nil {
			return "", total, e
		}
		in, e := root.Open(f.Path)
		if e != nil {
			return "", total, e
		}
		before, e := in.Stat()
		if e != nil {
			in.Close()
			return "", total, e
		}
		out, e := dst.OpenFile(f.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			in.Close()
			return "", total, e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, manifest.MaxFile+1))
		after, se := in.Stat()
		in.Close()
		if e == nil {
			e = out.Sync()
		}
		out.Close()
		fault.Hit("snapshot_file")
		if e != nil {
			return "", total, e
		}
		if se != nil || n != f.Size || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			return "", total, fmt.Errorf("source changed during copy: %s", f.Path)
		}
		got := hex.EncodeToString(h.Sum(nil))
		if f.SHA256 != "" && got != f.SHA256 {
			return "", total, fmt.Errorf("hash mismatch: %s", f.Path)
		}
		sel.Files[i].SHA256 = got
		if e = dst.Chmod(f.Path, 0400); e != nil {
			return "", total, e
		}
	}
	m := manifest.Manifest{Schema: 1, Name: sel.Name, Source: sel.Source, Files: sel.Files, Provenance: sel.Provenance}
	b, e := m.Bytes()
	if e != nil {
		return "", total, e
	}
	id := manifest.Digest(b)
	f, e := os.OpenFile(filepath.Join(tmp, "manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if e != nil {
		return "", total, e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	if e != nil {
		return "", total, e
	}
	fault.Hit("snapshot_manifest")
	var dirs []string
	filepath.WalkDir(tmp, func(p string, d fs.DirEntry, e error) error {
		if e == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return e
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, p := range dirs {
		if e = syncDir(p); e != nil {
			return "", total, e
		}
	}
	final := filepath.Join(s.Dir, "snapshots", id)
	if _, e = os.Stat(final); os.IsNotExist(e) {
		if e = os.Rename(tmp, final); e != nil {
			return "", total, e
		}
		if e = syncDir(filepath.Dir(final)); e != nil {
			return "", total, e
		}
	} else if e != nil {
		return "", total, e
	}
	fault.Hit("snapshot_rename")
	e = s.registerSnapshot(id, m)
	fault.Hit("snapshot_journal")
	return id, total, e
}
func (s *Spool) Manifest(id string) (manifest.Manifest, []byte, error) {
	if len(id) != 64 || strings.ContainsAny(id, "/\\.") {
		return manifest.Manifest{}, nil, errors.New("invalid bundle id")
	}
	b, e := os.ReadFile(filepath.Join(s.Dir, "snapshots", id, "manifest.json"))
	if e != nil {
		return manifest.Manifest{}, nil, e
	}
	m, e := manifest.Parse(b)
	if e == nil && manifest.Digest(b) != id {
		e = errors.New("snapshot manifest corrupted")
	}
	return m, b, e
}
func (s *Spool) Bundles() ([]Bundle, error) {
	rows, e := s.DB.Query(`SELECT id,name,state,ingestion,error,receipt,published FROM bundles ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	a := []Bundle{}
	for rows.Next() {
		var b Bundle
		var receipt []byte
		if e = rows.Scan(&b.ID, &b.Name, &b.State, &b.Ingestion, &b.Error, &receipt, &b.Published); e != nil {
			return nil, e
		}
		b.Receipt = receipt
		a = append(a, b)
	}
	return a, rows.Err()
}
func (s *Spool) Set(id, state, ing, errText string) error {
	_, e := s.DB.Exec(`UPDATE bundles SET state=?,ingestion=?,error=? WHERE id=?`, state, ing, errText, id)
	return e
}
func (s *Spool) Published(id string, receipt []byte) error {
	_, e := s.DB.Exec(`UPDATE bundles SET state='PUBLISHED',receipt=?,published=?,error='' WHERE id=?`, receipt, time.Now().Unix(), id)
	return e
}
func (s *Spool) Transfer(id, path, url string, offset int64) error {
	_, e := s.DB.Exec(`INSERT INTO transfers(bundle,path,url,offset) VALUES(?,?,?,?) ON CONFLICT(bundle,path) DO UPDATE SET url=excluded.url,offset=excluded.offset`, id, path, url, offset)
	return e
}
func (s *Spool) Prune() (int, error) {
	unlock, e := s.Lock("sync")
	if e != nil {
		return 0, e
	}
	defer unlock()
	u, e := s.Lock("prepare")
	if e != nil {
		return 0, e
	}
	defer u()
	bs, e := s.Bundles()
	if e != nil {
		return 0, e
	}
	n := 0
	for _, b := range bs {
		if b.State == "PUBLISHED" && b.Published <= time.Now().Add(-24*time.Hour).Unix() {
			if e = os.RemoveAll(filepath.Join(s.Dir, "snapshots", b.ID)); e != nil {
				return n, e
			}
			if _, e = s.DB.Exec(`UPDATE bundles SET state='PRUNED' WHERE id=?`, b.ID); e != nil {
				return n, e
			}
			n++
		}
	}
	return n, nil
}
