package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/samkwak188/labrelay/internal/fault"
)

func (s *Server) Run(ctx context.Context) {
	// Drain tusd notifications independently of the verifier: its channel sends
	// must never block the PATCH response while the worker waits on that upload.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.Tus.CompleteUploads:
				s.signal()
			}
		}
	}()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.wake:
		}
		if s.closed.Load() {
			return
		}
		s.lockMu.Lock()
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		e := s.lock.Ping(c)
		cancel()
		s.lockMu.Unlock()
		if e != nil {
			s.closed.Store(true)
			close(s.Stopped)
			slog.Error("exclusive_catalog_lock_lost", "error", e)
			return
		}
		m, e := s.maintenance(ctx)
		if e != nil || m {
			continue
		}
		if e = s.Reconcile(ctx); e != nil {
			slog.Warn("reconciliation_failed", "error", e)
		} else {
			s.heartbeat.Set(float64(time.Now().Unix()))
		}
	}
}
func missing(e error) bool {
	var a smithy.APIError
	if errors.As(e, &a) {
		return a.ErrorCode() == "NotFound" || a.ErrorCode() == "NoSuchKey" || a.ErrorCode() == "404"
	}
	return false
}
func (s *Server) Reconcile(ctx context.Context) error {
	// Expiration uses the same row locks as publication and records an event.
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `UPDATE ingestions SET state='EXPIRED',error='ingestion lifetime exceeded' WHERE state IN ('STAGING','VERIFYING') AND expires_at<=clock_timestamp() RETURNING id`)
	if e != nil {
		return e
	}
	var expired []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		expired = append(expired, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range expired {
		if _, e = tx.Exec(ctx, `INSERT INTO events(ingestion_id,kind) VALUES($1,'expired')`, id); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE attempts a SET state='TOMBSTONED' FROM files f,ingestions i WHERE a.file_id=f.id AND f.ingestion_id=i.id AND i.state IN ('FAILED','EXPIRED') AND a.state NOT IN ('TOMBSTONED','CLEANED')`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE attempts SET state='TOMBSTONED',error='creation did not expose an upload URL' WHERE state='CREATING' AND created_at<now()-interval '2 minutes'`); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	rows, e = s.DB.Query(ctx, `SELECT a.id,a.file_id,a.object_key,f.sha256,f.size_bytes,f.ingestion_id FROM attempts a JOIN files f ON f.id=a.file_id JOIN ingestions i ON i.id=f.ingestion_id WHERE a.state='ACTIVE' AND i.state IN ('STAGING','VERIFYING') ORDER BY a.created_at`)
	if e != nil {
		return e
	}
	type work struct {
		aid, fid, key, hash, ing string
		size                     int64
	}
	jobs := []work{}
	for rows.Next() {
		var j work
		if e = rows.Scan(&j.aid, &j.fid, &j.key, &j.hash, &j.size, &j.ing); e != nil {
			rows.Close()
			return e
		}
		jobs = append(jobs, j)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		gate := s.gate(j.aid)
		gate.Lock()
		e = s.verify(ctx, j.aid, j.fid, j.key, j.hash, j.ing, j.size)
		gate.Unlock()
		if e != nil && !missing(e) {
			s.DB.Exec(ctx, `UPDATE attempts SET error=$2 WHERE id=$1 AND state='ACTIVE'`, j.aid, e.Error())
			slog.Warn("verification_retry", "attempt_id", j.aid, "error", e)
		}
	}
	rows, e = s.DB.Query(ctx, `SELECT id FROM ingestions WHERE state IN ('STAGING','VERIFYING') AND finalize_requested ORDER BY created_at`)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = s.publish(ctx, id); e != nil {
			return e
		}
	}
	rows, e = s.DB.Query(ctx, `SELECT id,object_key FROM attempts WHERE state='TOMBSTONED' ORDER BY created_at LIMIT 100`)
	if e != nil {
		return e
	}
	type garbage struct{ id, key string }
	var trash []garbage
	for rows.Next() {
		var t garbage
		if e = rows.Scan(&t.id, &t.key); e != nil {
			rows.Close()
			return e
		}
		trash = append(trash, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, t := range trash {
		g := s.gate(t.id)
		g.Lock()
		e = s.cleanup(ctx, t.id, t.key)
		g.Unlock()
		if e != nil {
			slog.Warn("cleanup_retry", "attempt_id", t.id, "error", e)
		}
	}
	var n int
	e = s.DB.QueryRow(ctx, `SELECT count(*) FROM ingestions WHERE state IN ('STAGING','VERIFYING')`).Scan(&n)
	s.backlog.Set(float64(n))
	if e != nil {
		return e
	}
	e = s.DB.QueryRow(ctx, `SELECT count(*) FROM files f JOIN ingestions i ON i.id=f.ingestion_id WHERE i.state IN ('STAGING','VERIFYING') AND i.finalize_requested AND f.verified_at IS NULL`).Scan(&n)
	s.verificationPending.Set(float64(n))
	return e
}
func (s *Server) verify(ctx context.Context, aid, fid, key, expected, ing string, size int64) error {
	started := time.Now()
	var active bool
	e := s.DB.QueryRow(ctx, `SELECT a.state='ACTIVE' AND i.state IN ('STAGING','VERIFYING') AND i.expires_at>clock_timestamp() FROM attempts a JOIN files f ON f.id=a.file_id JOIN ingestions i ON i.id=f.ingestion_id WHERE a.id=$1`, aid).Scan(&active)
	if e != nil || !active {
		return e
	}
	head, e := s.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.C.Bucket), Key: aws.String(key)})
	if e != nil {
		return e
	}
	version := aws.ToString(head.VersionId)
	if version == "" || version == "null" {
		return errors.New("storage returned no immutable version ID")
	}
	if aws.ToInt64(head.ContentLength) != size {
		e = ErrIntegrity
	} else {
		e = s.VerifyVersion(ctx, key, version, expected, size)
	}
	if errors.Is(e, ErrIntegrity) {
		s.failures.Inc()
		tx, te := s.DB.Begin(ctx)
		if te != nil {
			return te
		}
		defer tx.Rollback(ctx)
		var state string
		if te = tx.QueryRow(ctx, `SELECT state FROM ingestions WHERE id=$1 FOR UPDATE`, ing).Scan(&state); te != nil {
			return te
		}
		if state == "READY" {
			return errors.New("cannot fail a published ingestion")
		}
		if _, te = tx.Exec(ctx, `UPDATE ingestions SET state='FAILED',error=$2 WHERE id=$1 AND state IN ('STAGING','VERIFYING')`, ing, e.Error()); te != nil {
			return te
		}
		if _, te = tx.Exec(ctx, `INSERT INTO events(ingestion_id,kind,detail) VALUES($1,'integrity_failed',$2)`, ing, fid); te != nil {
			return te
		}
		return tx.Commit(ctx)
	}
	if e != nil {
		return e
	}
	fault.Hit("object_verified")
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var allowed bool
	e = tx.QueryRow(ctx, `SELECT state IN ('STAGING','VERIFYING') AND expires_at>clock_timestamp() FROM ingestions WHERE id=$1 FOR UPDATE`, ing).Scan(&allowed)
	if e != nil || !allowed {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE attempts SET state='VERIFIED',error='' WHERE id=$1 AND state='ACTIVE'`, aid)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return nil
	}
	if _, e = tx.Exec(ctx, `UPDATE files SET object_key=$2,version_id=$3,verified_at=now() WHERE id=$1`, fid, key, version); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	s.verified.Add(float64(size))
	s.verifiedFiles.Inc()
	s.verifySeconds.Observe(time.Since(started).Seconds())
	return nil
}
func (s *Server) publish(ctx context.Context, id string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var state string
	var requested, expired bool
	var deadline time.Time
	if e = tx.QueryRow(ctx, `SELECT state,finalize_requested,expires_at FROM ingestions WHERE id=$1 FOR UPDATE`, id).Scan(&state, &requested, &deadline); e != nil {
		return e
	}
	// PostgreSQL can evaluate SELECT expressions before LockRows waits. Check the
	// database clock in a separate statement after acquiring the ingestion lock.
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()>=$1::timestamptz`, deadline).Scan(&expired); e != nil {
		return e
	}
	if state == "READY" || state == "FAILED" || state == "EXPIRED" || !requested || expired {
		return nil
	}
	var pending int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM files WHERE ingestion_id=$1 AND (verified_at IS NULL OR object_key IS NULL OR version_id IS NULL)`, id).Scan(&pending); e != nil || pending != 0 {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE ingestions SET state='READY',revision_id=$2,published_at=clock_timestamp(),error='' WHERE id=$1 AND expires_at>clock_timestamp()`, id, uuid.NewString())
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return nil
	}
	if _, e = tx.Exec(ctx, `INSERT INTO events(ingestion_id,kind) VALUES($1,'published')`, id); e != nil {
		return e
	}
	fault.Hit("before_publication_commit")
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	fault.Hit("after_publication_commit")
	return nil
}
func (s *Server) cleanup(ctx context.Context, id, key string) error {
	var eligible bool
	e := s.DB.QueryRow(ctx, `SELECT a.state='TOMBSTONED' AND i.state<>'READY' FROM attempts a JOIN files f ON f.id=a.file_id JOIN ingestions i ON i.id=f.ingestion_id WHERE a.id=$1`, id).Scan(&eligible)
	if e != nil || !eligible {
		return e
	}
	// Only the exact tombstoned key and its tus support objects may be removed.
	page := s3.NewListMultipartUploadsPaginator(s.S3, &s3.ListMultipartUploadsInput{Bucket: aws.String(s.C.Bucket), Prefix: aws.String(key)})
	for page.HasMorePages() {
		p, e := page.NextPage(ctx)
		if e != nil {
			return e
		}
		for _, u := range p.Uploads {
			if aws.ToString(u.Key) == key {
				if _, e = s.S3.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(s.C.Bucket), Key: u.Key, UploadId: u.UploadId}); e != nil {
					return e
				}
			}
		}
	}
	vp := s3.NewListObjectVersionsPaginator(s.S3, &s3.ListObjectVersionsInput{Bucket: aws.String(s.C.Bucket), Prefix: aws.String(key)})
	for vp.HasMorePages() {
		p, e := vp.NextPage(ctx)
		if e != nil {
			return e
		}
		del := func(k, v *string) error {
			if aws.ToString(k) != key && aws.ToString(k) != key+".info" && aws.ToString(k) != key+".part" {
				return nil
			}
			_, e := s.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.C.Bucket), Key: k, VersionId: v})
			return e
		}
		for _, v := range p.Versions {
			if e = del(v.Key, v.VersionId); e != nil {
				return e
			}
		}
		for _, v := range p.DeleteMarkers {
			if e = del(v.Key, v.VersionId); e != nil {
				return e
			}
		}
	}
	_, e = s.DB.Exec(ctx, `UPDATE attempts SET state='CLEANED' WHERE id=$1 AND state='TOMBSTONED'`, id)
	return e
}
func (s *Server) ValidateRestore(ctx context.Context) error {
	m, e := s.maintenance(ctx)
	if e != nil {
		return e
	}
	if !m {
		return errors.New("enable maintenance before validating restore")
	}
	rows, e := s.DB.Query(ctx, `SELECT f.object_key,f.version_id,f.sha256,f.size_bytes FROM files f JOIN ingestions i ON i.id=f.ingestion_id WHERE i.state='READY'`)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var k, v, h string
		var n int64
		if e = rows.Scan(&k, &v, &h, &n); e != nil {
			return e
		}
		if e = s.VerifyVersion(ctx, k, v, h, n); e != nil {
			return fmt.Errorf("restore validation failed for %s: %w", k, e)
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	_, e = s.DB.Exec(ctx, `UPDATE settings SET value='false' WHERE key='maintenance'`)
	return e
}
