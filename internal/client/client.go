//go:build linux

package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bdragon300/tusgo"
	"github.com/google/uuid"
	"github.com/samkwak188/labrelay/internal/manifest"
	"github.com/samkwak188/labrelay/internal/server"
	"github.com/samkwak188/labrelay/internal/spool"
	"golang.org/x/sync/errgroup"
)

type Client struct {
	Base        *url.URL
	Project     string
	HTTP        *http.Client
	BytesSent   atomic.Int64
	UploadStart atomic.Int64
	UploadEnd   atomic.Int64
	Started     time.Time
}
type APIError struct {
	Status int
	Body   server.APIError
}

func (e *APIError) Error() string { return fmt.Sprintf("%s: %s", e.Body.Code, e.Body.Message) }

type transport struct {
	base       *url.URL
	token      string
	next       http.RoundTripper
	bytes      *atomic.Int64
	start, end *atomic.Int64
	origin     time.Time
}
type counting struct {
	io.ReadCloser
	n *atomic.Int64
}

func (r counting) Read(b []byte) (int, error) {
	n, e := r.ReadCloser.Read(b)
	r.n.Add(int64(n))
	return n, e
}
func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != t.base.Scheme || r.URL.Host != t.base.Host {
		return nil, errors.New("refusing cross-origin request")
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	if r.Method == "PATCH" && r.Body != nil {
		t.start.CompareAndSwap(0, time.Since(t.origin).Nanoseconds())
		r.Body = counting{r.Body, t.bytes}
	}
	resp, e := t.next.RoundTrip(r)
	if r.Method == "PATCH" {
		t.end.Store(time.Since(t.origin).Nanoseconds())
	}
	return resp, e
}
func New(raw, project, token string) (*Client, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("LABRELAY_SERVER must be an HTTP(S) origin")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("server URL must not contain a path")
	}
	if token == "" || !manifest.NameOK(project) {
		return nil, errors.New("LABRELAY_TOKEN and valid LABRELAY_PROJECT required")
	}
	c := &Client{Base: u, Project: project, Started: time.Now()}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 45 * time.Second
	c.HTTP = &http.Client{Transport: transport{u, token, tr, &c.BytesSent, &c.UploadStart, &c.UploadEnd, c.Started}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not allowed") }}
	return c, nil
}
func (c *Client) request(ctx context.Context, method, path string, b []byte) (*http.Response, error) {
	u, e := c.Base.Parse(path)
	if e != nil {
		return nil, e
	}
	r, e := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	if b != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, e := c.HTTP.Do(r)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		a := &APIError{Status: resp.StatusCode}
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a.Body) != nil {
			a.Body = server.APIError{Code: "http_error", Message: resp.Status, Retryable: resp.StatusCode >= 500 || resp.StatusCode == 429}
		}
		return nil, a
	}
	return resp, nil
}
func (c *Client) JSON(ctx context.Context, method, path string, b []byte, out any) error {
	r, e := c.request(ctx, method, path, b)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(out)
}
func transient(e error) bool {
	var a *APIError
	if errors.As(e, &a) {
		return a.Body.Retryable
	}
	return !errors.Is(e, server.ErrIntegrity)
}
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (c *Client) Sync(ctx context.Context, s *spool.Spool, id string, retry bool) error {
	if e := s.BindTarget(c.Base.String(), c.Project); e != nil {
		return e
	}
	m, b, e := s.Manifest(id)
	if e != nil {
		return e
	}
	var st server.State
	path := "/v1/projects/" + c.Project + "/datasets/" + m.Name + "/ingestions"
	if retry {
		path += "?retry=true"
	}
	for attempt := 0; attempt < 10; attempt++ {
		e = c.JSON(ctx, "POST", path, b, &st)
		if e == nil && st.State == "READY" {
			receipt, _ := json.Marshal(st)
			return s.Published(id, receipt)
		}
		if e == nil {
			e = s.Set(id, "SYNCING", st.ID, "")
		}
		if e == nil {
			g, gctx := errgroup.WithContext(ctx)
			g.SetLimit(2)
			for _, f := range st.Files {
				if f.Verified {
					continue
				}
				f := f
				g.Go(func() error { return c.transfer(gctx, s, id, st.ID, f) })
			}
			e = g.Wait()
		}
		if e == nil {
			e = c.JSON(ctx, "POST", "/v1/ingestions/"+st.ID+"/finalize", nil, &st)
		}
		if e == nil {
			deadline := time.Now().Add(60 * time.Second)
			for st.State != "READY" {
				if time.Now().After(deadline) {
					e = &APIError{Status: 503, Body: server.APIError{Code: "verification_pending", Message: "verification still pending; progress retained", Retryable: true}}
					break
				}
				if st.State == "FAILED" || st.State == "EXPIRED" {
					e = &APIError{Status: 409, Body: server.APIError{Code: strings.ToLower(st.State), Message: st.Error}}
					break
				}
				if e = pause(ctx, time.Second); e != nil {
					break
				}
				if e = c.JSON(ctx, "GET", "/v1/ingestions/"+st.ID, nil, &st); e != nil {
					break
				}
			}
			if e == nil {
				receipt, _ := json.Marshal(st)
				return s.Published(id, receipt)
			}
		}
		if e == nil {
			return nil
		}
		s.Set(id, "ERROR", st.ID, e.Error())
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !transient(e) {
			return e
		}
		d := time.Duration(1<<min(attempt, 6)) * time.Second
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		if e2 := pause(ctx, time.Duration(rand.Int64N(int64(d)+1))); e2 != nil {
			return e2
		}
	}
	return fmt.Errorf("retry budget exhausted, rerun sync to resume: %w", e)
}
func (c *Client) transfer(ctx context.Context, s *spool.Spool, bid, ing string, f server.FileState) error {
	root, e := os.OpenRoot(filepath.Join(s.Dir, "snapshots", bid, "files"))
	if e != nil {
		return e
	}
	defer root.Close()
	in, e := root.Open(f.Path)
	if e != nil {
		return e
	}
	defer in.Close()
	h := sha256.New()
	n, e := io.Copy(h, in)
	if e != nil {
		return e
	}
	if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.Hash {
		return server.ErrIntegrity
	}
	var a struct {
		URL string `json:"url"`
	}
	if e = c.JSON(ctx, "POST", "/v1/ingestions/"+ing+"/files/"+f.ID+"/attempts", nil, &a); e != nil {
		return e
	}
	if e = s.Transfer(bid, f.Path, a.URL, 0); e != nil {
		return e
	}
	tc := tusgo.NewClient(c.HTTP, c.Base).WithContext(ctx)
	u := &tusgo.Upload{Location: a.URL, RemoteSize: f.Size}
	us := tusgo.NewUploadStream(tc, u).WithContext(ctx)
	us.ChunkSize = manifest.PatchSize
	resp, e := us.Sync()
	if e != nil {
		// Another collector or the verifier may already have completed this file.
		var latest server.State
		if c.JSON(ctx, "GET", "/v1/ingestions/"+ing, nil, &latest) == nil {
			for _, lf := range latest.Files {
				if lf.ID == f.ID && lf.Verified {
					return nil
				}
			}
		}
		if resp != nil && resp.StatusCode < 500 && resp.StatusCode != 409 && resp.StatusCode != 429 {
			return &APIError{Status: resp.StatusCode, Body: server.APIError{Code: "upload_rejected", Message: resp.Status}}
		}
		return e
	}
	if u.RemoteOffset < 0 || u.RemoteOffset > f.Size || u.RemoteSize != f.Size {
		return errors.New("invalid remote upload size/offset")
	}
	if _, e = in.Seek(u.RemoteOffset, io.SeekStart); e != nil {
		return e
	}
	if _, e = us.ReadFrom(in); e != nil {
		return e
	}
	return s.Transfer(bid, f.Path, a.URL, u.RemoteOffset)
}
func (c *Client) Fetch(ctx context.Context, id, dest string) error {
	var st server.State
	if e := c.JSON(ctx, "GET", "/v1/revisions/"+url.PathEscape(id), nil, &st); e != nil {
		return e
	}
	m, e := manifest.Parse(st.Manifest)
	if e != nil {
		return e
	}
	if st.State != "READY" || manifest.Digest(st.Manifest) != st.Digest {
		return errors.New("invalid publication manifest/receipt")
	}
	expected := map[string]manifest.File{}
	for _, f := range m.Files {
		expected[f.Path] = f
	}
	if len(st.Files) != len(expected) {
		return errors.New("published file set mismatch")
	}
	for _, f := range st.Files {
		x, ok := expected[f.Path]
		if !ok || !f.Verified || x.Size != f.Size || x.SHA256 != f.Hash {
			return errors.New("published file metadata mismatch")
		}
		delete(expected, f.Path)
	}
	dest, e = filepath.Abs(dest)
	if e != nil {
		return e
	}
	parent, e := os.OpenRoot(filepath.Dir(dest))
	if e != nil {
		return e
	}
	defer parent.Close()
	name := filepath.Base(dest)
	if e = parent.Mkdir(name, 0700); e != nil {
		return fmt.Errorf("destination must not exist: %w", e)
	}
	// Use a private staging directory whose name cannot collide with a manifest path.
	root, e := parent.OpenRoot(name)
	if e != nil {
		return e
	}
	defer root.Close()
	stage := ".labrelay-incomplete-" + uuid.NewString()
	for _, f := range m.Files {
		if f.Path == stage || strings.HasPrefix(f.Path, stage+"/") {
			return errors.New("temporary path collision; retry with a new destination")
		}
	}
	if e = root.Mkdir(stage, 0700); e != nil {
		return e
	}
	for _, f := range st.Files {
		if e = root.MkdirAll(filepath.Dir(f.Path), 0700); e != nil {
			return e
		}
		tmp := stage + "/" + uuid.NewString()
		out, e := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		resp, e := c.request(ctx, "GET", "/v1/revisions/"+id+"/files/"+f.ID, nil)
		if e != nil {
			out.Close()
			return e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, f.Size+1))
		resp.Body.Close()
		if e == nil {
			e = out.Sync()
		}
		out.Close()
		if e != nil {
			return e
		}
		if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.Hash {
			return server.ErrIntegrity
		}
		// Link is atomic and fails if a destination appeared; never overwrite user files.
		if e = root.Link(tmp, f.Path); e != nil {
			return e
		}
		if e = root.Remove(tmp); e != nil {
			return e
		}
		dir, err := root.Open(filepath.Dir(f.Path))
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return err
		}
	}
	if e = root.Remove(stage); e != nil {
		return e
	}
	d, e := root.Open(".")
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
