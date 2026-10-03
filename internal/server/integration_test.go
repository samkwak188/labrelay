//go:build linux

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/samkwak188/labrelay/internal/client"
	"github.com/samkwak188/labrelay/internal/manifest"
	"github.com/samkwak188/labrelay/internal/server"
	"github.com/samkwak188/labrelay/internal/spool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIntegration(t *testing.T) {
	if os.Getenv("LABRELAY_INTEGRATION") != "1" {
		t.Skip("requires PostgreSQL and versioned S3; scripts/test.sh integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg := server.EnvConfig()
	cfg.Temp = t.TempDir()
	p, e := server.Pool(ctx, cfg)
	must(t, e)
	must(t, server.Migrate(ctx, p))
	if cfg.Endpoint != "" {
		must(t, server.InitStorage(ctx, cfg))
	}
	project := "test-" + uuid.NewString()
	token, e := server.Token(ctx, p, project, "write", "")
	must(t, e)
	other, e := server.Token(ctx, p, "other-"+uuid.NewString(), "write", "")
	must(t, e)
	p.Close()
	s, e := server.New(ctx, cfg)
	must(t, e)
	defer s.Close()
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	c, e := client.New(hs.URL, project, token)
	must(t, e)
	var done = make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	base := t.TempDir()
	src := filepath.Join(base, "source")
	must(t, os.Mkdir(src, 0700))
	want := map[string][]byte{"zero": {}, "small": []byte("unchanged bytes"), "nested/multipart": make([]byte, (8<<20)+19)}
	for i := range want["nested/multipart"] {
		want["nested/multipart"][i] = byte(i*73 + 19)
	}
	for path, b := range want {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(src, path)), 0700))
		must(t, os.WriteFile(filepath.Join(src, path), b, 0600))
	}
	sp, e := spool.Open(filepath.Join(base, "spool"))
	must(t, e)
	defer sp.Close()
	sel, e := spool.Generic(src, "bundle")
	must(t, e)
	id, _, e := sp.Prepare(ctx, src, sel, false)
	must(t, e)
	must(t, c.Sync(ctx, sp, id, false))
	bs, e := sp.Bundles()
	must(t, e)
	if len(bs) != 1 || bs[0].State != "PUBLISHED" {
		t.Fatal(bs)
	}
	var receipt server.State
	must(t, json.Unmarshal(bs[0].Receipt, &receipt))
	if len(receipt.Files) != 3 {
		t.Fatal(receipt)
	}
	t.Run("exact-version-and-no-overwrite", func(t *testing.T) {
		for _, f := range receipt.Files {
			if f.Version == "" || f.Version == "null" || f.Bucket != cfg.Bucket {
				t.Fatalf("missing exact object tuple: %+v", f)
			}
			_, e := s.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(f.Key), Body: bytes.NewReader([]byte("newer unrelated version"))})
			must(t, e)
		}
		dest := filepath.Join(base, "fetch")
		must(t, c.Fetch(ctx, receipt.Revision, dest))
		for p, b := range want {
			got, e := os.ReadFile(filepath.Join(dest, p))
			must(t, e)
			if !bytes.Equal(got, b) {
				t.Fatal("wrong pinned version", p)
			}
		}
		if e := c.Fetch(ctx, receipt.Revision, dest); e == nil {
			t.Fatal("existing destination overwritten")
		}
	})
	t.Run("duplicate-registration-and-finalization", func(t *testing.T) {
		_, b, e := sp.Manifest(id)
		must(t, e)
		var st server.State
		must(t, c.JSON(ctx, "POST", "/v1/projects/"+project+"/datasets/bundle/ingestions", b, &st))
		if st.Revision != receipt.Revision {
			t.Fatal("duplicate revision")
		}
		must(t, c.JSON(ctx, "POST", "/v1/ingestions/"+st.ID+"/finalize", nil, &st))
		if st.Revision != receipt.Revision {
			t.Fatal("receipt changed")
		}
	})
	t.Run("authorization-including-head", func(t *testing.T) {
		for _, tok := range []string{"invalid", other} {
			r, _ := http.NewRequestWithContext(ctx, "HEAD", hs.URL+"/uploads/"+receipt.Files[0].UploadID, nil)
			r.Header.Set("Authorization", "Bearer "+tok)
			r.Header.Set("Tus-Resumable", "1.0.0")
			resp, e := http.DefaultClient.Do(r)
			must(t, e)
			resp.Body.Close()
			if resp.StatusCode != 401 && resp.StatusCode != 404 && resp.StatusCode != 403 {
				t.Fatal(resp.Status)
			}
		}
	})
	t.Run("incomplete-never-publishes-and-creation-idempotent", func(t *testing.T) {
		m := manifest.Manifest{Schema: 1, Name: "incomplete", Source: "generic", Files: []manifest.File{{Path: "a", Role: "data", Size: 10, SHA256: manifest.Digest([]byte("0123456789"))}}}
		b, e := m.Bytes()
		must(t, e)
		var st server.State
		must(t, c.JSON(ctx, "POST", "/v1/projects/"+project+"/datasets/incomplete/ingestions", b, &st))
		path := "/v1/ingestions/" + st.ID + "/files/" + st.Files[0].ID + "/attempts"
		var a, both map[string]string
		must(t, c.JSON(ctx, "POST", path, nil, &a))
		must(t, c.JSON(ctx, "POST", path, nil, &both))
		if a["url"] != both["url"] {
			t.Fatal("creation changed URL")
		}
		must(t, c.JSON(ctx, "POST", "/v1/ingestions/"+st.ID+"/finalize", nil, &st))
		if st.State == "READY" {
			t.Fatal("incomplete published")
		}
		r, _ := http.NewRequestWithContext(ctx, "PATCH", hs.URL+a["url"], bytes.NewReader([]byte("wrongbytes")))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Tus-Resumable", "1.0.0")
		r.Header.Set("Upload-Offset", "0")
		r.Header.Set("Content-Type", "application/offset+octet-stream")
		resp, e := http.DefaultClient.Do(r)
		must(t, e)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Fatal(resp.Status)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			must(t, c.JSON(ctx, "GET", "/v1/ingestions/"+st.ID, nil, &st))
			if st.State == "FAILED" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("corruption did not fail", st.State)
	})
	t.Run("expiration-cleanup-preserves-publication", func(t *testing.T) {
		m := manifest.Manifest{Schema: 1, Name: "expire", Source: "generic", Files: []manifest.File{{Path: "a", Role: "data", Size: 4, SHA256: manifest.Digest([]byte("data"))}}}
		b, e := m.Bytes()
		must(t, e)
		var st server.State
		path := "/v1/projects/" + project + "/datasets/expire/ingestions"
		must(t, c.JSON(ctx, "POST", path, b, &st))
		var a map[string]string
		must(t, c.JSON(ctx, "POST", "/v1/ingestions/"+st.ID+"/files/"+st.Files[0].ID+"/attempts", nil, &a))
		_, e = s.DB.Exec(ctx, `UPDATE ingestions SET expires_at=now()-interval '1 second' WHERE id=$1`, st.ID)
		must(t, e)
		r, _ := http.NewRequestWithContext(ctx, "HEAD", hs.URL+a["url"], nil)
		r.Header.Set("Tus-Resumable", "1.0.0")
		r.Header.Set("Authorization", "Bearer "+token)
		resp, e := http.DefaultClient.Do(r)
		must(t, e)
		resp.Body.Close()
		if resp.StatusCode != 409 {
			t.Fatal("expired upload writable", resp.Status)
		}
		deadline := time.Now().Add(20 * time.Second)
		cleaned := false
		for time.Now().Before(deadline) {
			var state string
			must(t, s.DB.QueryRow(ctx, `SELECT state FROM attempts WHERE id=$1`, a["attempt_id"]).Scan(&state))
			if state == "CLEANED" {
				cleaned = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !cleaned {
			t.Fatal("cleanup did not finish")
		}
		for _, f := range receipt.Files {
			_, e = s.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(f.Bucket), Key: aws.String(f.Key), VersionId: aws.String(f.Version)})
			must(t, e)
		}
		if e = c.JSON(ctx, "POST", path, b, &st); e == nil {
			t.Fatal("terminal session silently retried")
		}
		old := st.ID
		must(t, c.JSON(ctx, "POST", path+"?retry=true", b, &st))
		if st.ID == old {
			t.Fatal("explicit retry did not create a new session")
		}
	})
	t.Run("maintenance-and-restore-validation", func(t *testing.T) {
		_, e = s.DB.Exec(ctx, `UPDATE settings SET value='true' WHERE key='maintenance'`)
		must(t, e)
		var data any
		if e = c.JSON(ctx, "GET", "/v1/revisions/"+receipt.Revision, nil, &data); e == nil {
			t.Fatal("downloads exposed in maintenance")
		}
		must(t, s.ValidateRestore(ctx))
		must(t, c.JSON(ctx, "GET", "/v1/revisions/"+receipt.Revision, nil, &data))
	})
	t.Run("revocation", func(t *testing.T) {
		_, e := s.DB.Exec(ctx, `UPDATE tokens SET revoked=true WHERE hash=$1`, manifest.Digest([]byte(token)))
		must(t, e)
		var out any
		if e = c.JSON(ctx, "GET", "/v1/revisions/"+receipt.Revision, nil, &out); e == nil {
			t.Fatal("revoked token allowed")
		}
	})
	t.Logf("verified %d-byte multipart file plus empty/small files on %s", len(want["nested/multipart"]), cfg.Endpoint)
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(fmt.Errorf("operation failed: %w", e))
	}
}
