package server

import (
	"context"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
)

// AuditStorage reports catalog-unknown object keys and multipart uploads. It never deletes.
func (s *Server) AuditStorage(ctx context.Context, w io.Writer) error {
	rows, e := s.DB.Query(ctx, `SELECT object_key FROM attempts`)
	if e != nil {
		return e
	}
	known := map[string]bool{}
	for rows.Next() {
		var k string
		if e = rows.Scan(&k); e != nil {
			rows.Close()
			return e
		}
		known[k] = true
		known[k+".info"] = true
		known[k+".part"] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	enc := json.NewEncoder(w)
	versions := s3.NewListObjectVersionsPaginator(s.S3, &s3.ListObjectVersionsInput{Bucket: aws.String(s.C.Bucket)})
	for versions.HasMorePages() {
		p, e := versions.NextPage(ctx)
		if e != nil {
			return e
		}
		for _, v := range p.Versions {
			if !known[aws.ToString(v.Key)] {
				if e = enc.Encode(map[string]any{"kind": "unknown_version", "key": v.Key, "version_id": v.VersionId, "size": v.Size}); e != nil {
					return e
				}
			}
		}
	}
	multipart := s3.NewListMultipartUploadsPaginator(s.S3, &s3.ListMultipartUploadsInput{Bucket: aws.String(s.C.Bucket)})
	for multipart.HasMorePages() {
		p, e := multipart.NextPage(ctx)
		if e != nil {
			return e
		}
		for _, u := range p.Uploads {
			if !known[aws.ToString(u.Key)] {
				if e = enc.Encode(map[string]any{"kind": "unknown_multipart", "key": u.Key, "upload_id": u.UploadId}); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
