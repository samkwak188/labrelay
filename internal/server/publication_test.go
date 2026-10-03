package server

import (
	"bytes"
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/samkwak188/labrelay/internal/manifest"
	"os"
	"testing"
	"time"
)

func TestPublicationDeadlineAfterLockWait(t *testing.T) {
	if os.Getenv("LABRELAY_INTEGRATION") != "1" {
		t.Skip("requires PostgreSQL/S3")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := EnvConfig()
	cfg.Temp = t.TempDir()
	p, e := Pool(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = Migrate(ctx, p); e != nil {
		p.Close()
		t.Fatal(e)
	}
	p.Close()
	if cfg.Endpoint != "" {
		if e = InitStorage(ctx, cfg); e != nil {
			t.Fatal(e)
		}
	}
	s, e := New(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	id := uuid.NewString()
	key := "uploads/" + id
	data := []byte("deadline race")
	hash := manifest.Digest(data)
	obj, e := s.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(data)})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.VerifyVersion(ctx, key, aws.ToString(obj.VersionId), hash, int64(len(data))); e != nil {
		t.Fatal(e)
	}
	m := manifest.Manifest{Schema: 1, Name: "deadline", Source: "generic", Files: []manifest.File{{Path: "a", Role: "data", Size: int64(len(data)), SHA256: hash}}}
	b, e := m.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(ctx, `INSERT INTO ingestions(id,project,name,digest,manifest,size_bytes,state,finalize_requested,expires_at) VALUES($1,$1,'deadline',$2,$3,$4,'VERIFYING',true,clock_timestamp()+interval '1 second')`, id, manifest.Digest(b), b, len(data))
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(ctx, `INSERT INTO files(id,ingestion_id,path,role,size_bytes,sha256,object_key,version_id,verified_at) VALUES($1,$1,'a','data',$2,$3,$4,$5,now())`, id, len(data), hash, key, aws.ToString(obj.VersionId))
	if e != nil {
		t.Fatal(e)
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT id FROM ingestions WHERE id=$1 FOR UPDATE`, id)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- s.publish(ctx, id) }()
	time.Sleep(1200 * time.Millisecond)
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	var state string
	if e = s.DB.QueryRow(ctx, `SELECT state FROM ingestions WHERE id=$1`, id).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state == "READY" {
		t.Fatal("published after deadline while waiting on the ingestion lock")
	}
}
