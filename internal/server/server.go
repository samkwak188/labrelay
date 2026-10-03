package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go/logging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/samkwak188/labrelay/internal/fault"
	"github.com/samkwak188/labrelay/internal/manifest"
	tus "github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
	"github.com/tus/tusd/v2/pkg/s3store"
)

//go:embed schema.sql
var schema string

type Config struct {
	Database, Bucket, Endpoint, Region, Temp string
	Quota                                    int64
}

func EnvConfig() Config {
	return Config{Database: os.Getenv("DATABASE_URL"), Bucket: os.Getenv("S3_BUCKET"), Endpoint: os.Getenv("S3_ENDPOINT"), Region: env("AWS_REGION", "us-east-2"), Temp: env("LABRELAY_TEMP", "/tmp/labrelay-upload"), Quota: 50 << 30}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func Pool(ctx context.Context, c Config) (*pgxpool.Pool, error) {
	cfg, e := pgxpool.ParseConfig(c.Database)
	if e != nil {
		return nil, e
	}
	cfg.MaxConns = 8
	return pgxpool.NewWithConfig(ctx, cfg)
}
func Migrate(ctx context.Context, p *pgxpool.Pool) error {
	tx, e := p.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(19374891)`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, schema); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func Storage(ctx context.Context, c Config) (*s3.Client, error) {
	cfg, e := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region), config.WithHTTPClient(&http.Client{Timeout: 5 * time.Minute}), config.WithLogger(logging.LoggerFunc(func(classification logging.Classification, format string, args ...interface{}) {
		if classification != logging.Debug {
			slog.Warn("aws_sdk", "message", fmt.Sprintf(format, args...))
		}
	})))
	if e != nil {
		return nil, e
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
			o.UsePathStyle = true
		}
	}), nil
}
func InitStorage(ctx context.Context, c Config) error {
	sc, e := Storage(ctx, c)
	if e != nil {
		return e
	}
	_, e = sc.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.Bucket)})
	if e != nil {
		in := &s3.CreateBucketInput{Bucket: aws.String(c.Bucket)}
		if c.Endpoint == "" && c.Region != "us-east-1" {
			in.CreateBucketConfiguration = &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint(c.Region)}
		}
		if _, e = sc.CreateBucket(ctx, in); e != nil {
			return e
		}
	}
	_, e = sc.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(c.Bucket), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}})
	return e
}
func Token(ctx context.Context, p *pgxpool.Pool, project, access, provided string) (string, error) {
	if !manifest.NameOK(project) || (access != "read" && access != "write") {
		return "", errors.New("invalid project or access")
	}
	if provided == "" {
		b := make([]byte, 32)
		if _, e := rand.Read(b); e != nil {
			return "", e
		}
		provided = hex.EncodeToString(b)
	}
	_, e := p.Exec(ctx, `INSERT INTO tokens(hash,project,access) VALUES($1,$2,$3) ON CONFLICT(hash) DO UPDATE SET revoked=false,project=excluded.project,access=excluded.access`, manifest.Digest([]byte(provided)), project, access)
	return provided, e
}

type Server struct {
	C                   Config
	DB                  *pgxpool.Pool
	S3                  *s3.Client
	Store               s3store.S3Store
	Tus                 *tus.Handler
	lock                *pgx.Conn
	lockMu              sync.Mutex
	gates               sync.Map
	createMu            sync.Mutex
	uploads             chan struct{}
	wake                chan struct{}
	closed              atomic.Bool
	closeOnce           sync.Once
	Stopped             chan struct{}
	registry            *prometheus.Registry
	verified            prometheus.Counter
	verifiedFiles       prometheus.Counter
	verificationPending prometheus.Gauge
	verifySeconds       prometheus.Histogram
	failures            prometheus.Counter
	heartbeat           prometheus.Gauge
	backlog             prometheus.Gauge
}

func New(ctx context.Context, c Config) (*Server, error) {
	if c.Database == "" || c.Bucket == "" {
		return nil, errors.New("DATABASE_URL and S3_BUCKET required")
	}
	if c.Quota == 0 {
		c.Quota = 50 << 30
	}
	p, e := Pool(ctx, c)
	if e != nil {
		return nil, e
	}
	sc, e := Storage(ctx, c)
	if e != nil {
		p.Close()
		return nil, e
	}
	lock, e := pgx.Connect(ctx, c.Database)
	if e != nil {
		p.Close()
		return nil, e
	}
	var ok bool
	if e = lock.QueryRow(ctx, `SELECT pg_try_advisory_lock(19374892)`).Scan(&ok); e != nil || !ok {
		lock.Close(ctx)
		p.Close()
		return nil, errors.New("another LabRelay server holds the exclusive catalog lock")
	}
	s := &Server{C: c, DB: p, S3: sc, lock: lock, uploads: make(chan struct{}, 4), wake: make(chan struct{}, 1), Stopped: make(chan struct{}), registry: prometheus.NewRegistry()}
	fail := func(e error) (*Server, error) { s.Close(); return nil, e }
	// Bind this catalog to one bucket. Switching buckets would invalidate every receipt.
	if _, e = p.Exec(ctx, `INSERT INTO settings(key,value) VALUES('bucket',$1) ON CONFLICT DO NOTHING`, c.Bucket); e != nil {
		return fail(e)
	}
	var catalogBucket string
	if e = p.QueryRow(ctx, `SELECT value FROM settings WHERE key='bucket'`).Scan(&catalogBucket); e != nil {
		return fail(e)
	}
	if catalogBucket != c.Bucket {
		return fail(errors.New("configured bucket differs from immutable catalog bucket"))
	}
	v, e := sc.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(c.Bucket)})
	if e != nil {
		return fail(e)
	}
	if v.Status != types.BucketVersioningStatusEnabled {
		return fail(errors.New("bucket versioning must be enabled"))
	}
	if e = os.MkdirAll(c.Temp, 0700); e != nil {
		return fail(e)
	}
	s.Store = s3store.New(c.Bucket, sc)
	s.Store.ObjectPrefix = "uploads/"
	s.Store.TemporaryDirectory = c.Temp
	s.Store.PreferredPartSize = 8 << 20
	s.Store.MaxBufferedParts = 1
	s.Store.SetConcurrentPartUploads(4)
	composer := tus.NewStoreComposer()
	s.Store.UseIn(composer)
	memorylocker.New().UseIn(composer)
	s.Tus, e = tus.NewHandler(tus.Config{BasePath: "/uploads/", StoreComposer: composer, MaxSize: manifest.MaxFile, DisableDownload: true, DisableTermination: true, DisableConcatenation: true, NotifyCompleteUploads: true, NetworkTimeout: 30 * time.Second, GracefulRequestCompletionTimeout: 10 * time.Second})
	if e != nil {
		return fail(e)
	}
	if _, e = p.Exec(ctx, `UPDATE attempts SET state='TOMBSTONED',error='unexposed creation interrupted' WHERE state='CREATING'`); e != nil {
		return fail(e)
	}
	s.verified = prometheus.NewCounter(prometheus.CounterOpts{Name: "labrelay_verified_bytes_total", Help: "Bytes independently hashed successfully"})
	s.verifiedFiles = prometheus.NewCounter(prometheus.CounterOpts{Name: "labrelay_verified_files_total", Help: "Files independently verified"})
	s.verificationPending = prometheus.NewGauge(prometheus.GaugeOpts{Name: "labrelay_verification_pending_files", Help: "Unverified files in ingestions requesting publication"})
	s.verifySeconds = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "labrelay_verification_seconds", Help: "Successful independent file verification duration", Buckets: []float64{.01, .1, .5, 1, 5, 15, 60, 300}})
	s.failures = prometheus.NewCounter(prometheus.CounterOpts{Name: "labrelay_integrity_failures_total", Help: "Integrity failures"})
	s.heartbeat = prometheus.NewGauge(prometheus.GaugeOpts{Name: "labrelay_reconciler_timestamp_seconds", Help: "Last successful reconciliation"})
	s.backlog = prometheus.NewGauge(prometheus.GaugeOpts{Name: "labrelay_pending_ingestions", Help: "Unpublished active ingestions"})
	s.registry.MustRegister(s.verified, s.verifiedFiles, s.verificationPending, s.verifySeconds, s.failures, s.heartbeat, s.backlog, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	s.Store.RegisterMetrics(s.registry)
	return s, nil
}
func (s *Server) Close() {
	s.closed.Store(true)
	s.closeOnce.Do(func() {
		s.lockMu.Lock()
		defer s.lockMu.Unlock()
		if s.lock != nil {
			s.lock.Close(context.Background())
		}
		if s.DB != nil {
			s.DB.Close()
		}
	})
}
func (s *Server) gate(id string) *sync.RWMutex {
	v, _ := s.gates.LoadOrStore(id, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}
func (s *Server) maintenance(ctx context.Context) (bool, error) {
	var v string
	e := s.DB.QueryRow(ctx, `SELECT value FROM settings WHERE key='maintenance'`).Scan(&v)
	return v != "false", e
}

type identity struct{ Project, Access string }
type identityKey struct{}

func ident(r *http.Request) identity { return r.Context().Value(identityKey{}).(identity) }

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	RequestID string `json:"request_id"`
}

func problem(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(APIError{code, msg, status == 429 || status >= 500, w.Header().Get("X-Request-ID")})
}
func output(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func (s *Server) internal(w http.ResponseWriter, r *http.Request, e error) {
	slog.Error("request_failed", "request_id", w.Header().Get("X-Request-ID"), "error", e)
	problem(w, r, 503, "dependency_unavailable", "operation could not complete; retry is safe")
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { output(w, map[string]bool{"live": true}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), 3*time.Second)
		defer c()
		m, e := s.maintenance(ctx)
		if e == nil && !m {
			e = s.DB.Ping(ctx)
		}
		if e == nil && !m {
			_, e = s.S3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.C.Bucket)})
		}
		if e != nil || m || s.closed.Load() {
			problem(w, r, 503, "not_ready", "maintenance or dependency unavailable")
			return
		}
		output(w, map[string]bool{"ready": true})
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	api := http.NewServeMux()
	api.HandleFunc("POST /v1/projects/{project}/datasets/{name}/ingestions", s.register)
	api.HandleFunc("GET /v1/ingestions/{id}", s.status)
	api.HandleFunc("POST /v1/ingestions/{id}/files/{file_id}/attempts", s.attempt)
	api.HandleFunc("POST /v1/ingestions/{id}/finalize", s.finalize)
	api.HandleFunc("GET /v1/projects/{project}/revisions", s.list)
	api.HandleFunc("GET /v1/revisions/{id}", s.revision)
	api.HandleFunc("GET /v1/revisions/{id}/files/{file_id}", s.download)
	api.HandleFunc("/uploads/{upload_id}", s.upload)
	auth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.closed.Load() {
			problem(w, r, 503, "not_ready", "server lock lost")
			return
		}
		m, e := s.maintenance(r.Context())
		if e != nil {
			s.internal(w, r, e)
			return
		}
		if m {
			problem(w, r, 503, "maintenance", "catalog is in maintenance mode")
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || token == r.Header.Get("Authorization") {
			problem(w, r, 401, "unauthorized", "bearer token required")
			return
		}
		var who identity
		e = s.DB.QueryRow(r.Context(), `SELECT project,access FROM tokens WHERE hash=$1 AND NOT revoked`, manifest.Digest([]byte(token))).Scan(&who.Project, &who.Access)
		if errors.Is(e, pgx.ErrNoRows) {
			problem(w, r, 401, "unauthorized", "invalid or revoked token")
			return
		}
		if e != nil {
			s.internal(w, r, e)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && who.Access != "write" {
			problem(w, r, 403, "forbidden", "write access required")
			return
		}
		api.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, who)))
	})
	mux.Handle("/v1/", auth)
	mux.Handle("/uploads/", auth)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

type FileState struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Size      int64  `json:"size_bytes"`
	Hash      string `json:"sha256"`
	Verified  bool   `json:"verified"`
	UploadID  string `json:"upload_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
	Error     string `json:"error,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Key       string `json:"object_key,omitempty"`
	Version   string `json:"version_id,omitempty"`
}
type State struct {
	ID        string      `json:"id"`
	Project   string      `json:"project"`
	Name      string      `json:"name"`
	Digest    string      `json:"manifest_sha256"`
	State     string      `json:"state"`
	Manifest  []byte      `json:"manifest_bytes"`
	Files     []FileState `json:"files"`
	Expires   time.Time   `json:"expires_at"`
	Revision  string      `json:"revision_id,omitempty"`
	Published *time.Time  `json:"published_at,omitempty"`
	Error     string      `json:"error,omitempty"`
}

func (s *Server) load(ctx context.Context, id, project string, revision bool) (State, error) {
	var st State
	col := "id"
	extra := ""
	if revision {
		col = "revision_id"
		extra = " AND state='READY'"
	}
	e := s.DB.QueryRow(ctx, `SELECT id,project,name,digest,state,manifest,expires_at,COALESCE(revision_id,''),published_at,error FROM ingestions WHERE `+col+`=$1 AND project=$2`+extra, id, project).Scan(&st.ID, &st.Project, &st.Name, &st.Digest, &st.State, &st.Manifest, &st.Expires, &st.Revision, &st.Published, &st.Error)
	if e != nil {
		return st, e
	}
	rows, e := s.DB.Query(ctx, `SELECT f.id,f.path,f.size_bytes,f.sha256,f.verified_at IS NOT NULL,COALESCE(a.upload_id,''),COALESCE(a.id,''),COALESCE(a.error,''),COALESCE(f.object_key,''),COALESCE(f.version_id,'') FROM files f LEFT JOIN attempts a ON a.file_id=f.id AND a.state IN ('CREATING','ACTIVE','VERIFIED') WHERE f.ingestion_id=$1 ORDER BY f.path`, st.ID)
	if e != nil {
		return st, e
	}
	defer rows.Close()
	st.Files = []FileState{}
	for rows.Next() {
		var f FileState
		if e = rows.Scan(&f.ID, &f.Path, &f.Size, &f.Hash, &f.Verified, &f.UploadID, &f.AttemptID, &f.Error, &f.Key, &f.Version); e != nil {
			return st, e
		}
		if f.Verified {
			f.Bucket = s.C.Bucket
		}
		st.Files = append(st.Files, f)
	}
	return st, rows.Err()
}
func (s *Server) respondState(w http.ResponseWriter, r *http.Request, id string, revision bool) {
	st, e := s.load(r.Context(), id, ident(r).Project, revision)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, r, 404, "not_found", "dataset not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	output(w, st)
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.respondState(w, r, r.PathValue("id"), false)
}
func (s *Server) revision(w http.ResponseWriter, r *http.Request) {
	s.respondState(w, r, r.PathValue("id"), true)
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	project, name := r.PathValue("project"), r.PathValue("name")
	if project != ident(r).Project {
		problem(w, r, 403, "forbidden", "project mismatch")
		return
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, manifest.MaxManifest))
	if e != nil {
		problem(w, r, 413, "manifest_limit", "manifest exceeds limit")
		return
	}
	m, e := manifest.Parse(b)
	if e != nil || m.Name != name {
		problem(w, r, 400, "invalid_manifest", "invalid manifest or dataset name")
		return
	}
	digest := manifest.Digest(b)
	tx, e := s.DB.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	// One small global reservation lock makes quota accounting and registration atomic.
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(19374893)`); e != nil {
		s.internal(w, r, e)
		return
	}
	var id string
	e = tx.QueryRow(r.Context(), `SELECT id FROM ingestions WHERE project=$1 AND name=$2 AND digest=$3 AND state IN ('STAGING','VERIFYING','READY')`, project, name, digest).Scan(&id)
	if e == nil {
		tx.Rollback(r.Context())
		s.respondState(w, r, id, false)
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		s.internal(w, r, e)
		return
	}
	var previous bool
	if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM ingestions WHERE project=$1 AND name=$2 AND digest=$3)`, project, name, digest).Scan(&previous); e != nil {
		s.internal(w, r, e)
		return
	}
	if previous && r.URL.Query().Get("retry") != "true" {
		problem(w, r, 409, "retry_required", "previous session failed or expired; explicitly retry after correcting the cause")
		return
	}
	var reserved int64
	if e = tx.QueryRow(r.Context(), `SELECT COALESCE(sum(size_bytes),0) FROM ingestions WHERE state IN ('STAGING','VERIFYING','READY')`).Scan(&reserved); e != nil {
		s.internal(w, r, e)
		return
	}
	if reserved+m.Size() > s.C.Quota {
		problem(w, r, 413, "quota_exceeded", "server dataset reservation quota exceeded")
		return
	}
	id = uuid.NewString()
	_, e = tx.Exec(r.Context(), `INSERT INTO ingestions(id,project,name,digest,manifest,size_bytes,state) VALUES($1,$2,$3,$4,$5,$6,'STAGING')`, id, project, name, digest, b, m.Size())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	for _, f := range m.Files {
		if _, e = tx.Exec(r.Context(), `INSERT INTO files(id,ingestion_id,path,role,size_bytes,sha256) VALUES($1,$2,$3,$4,$5,$6)`, uuid.NewString(), id, f.Path, f.Role, f.Size, f.SHA256); e != nil {
			s.internal(w, r, e)
			return
		}
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO events(ingestion_id,kind) VALUES($1,'registered')`, id)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.respondState(w, r, id, false)
}
func (s *Server) attempt(w http.ResponseWriter, r *http.Request) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	ctx := r.Context()
	ing, fid := r.PathValue("id"), r.PathValue("file_id")
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	var state string
	var expired bool
	e = tx.QueryRow(ctx, `SELECT state,expires_at<=clock_timestamp() FROM ingestions WHERE id=$1 AND project=$2 FOR UPDATE`, ing, ident(r).Project).Scan(&state, &expired)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, r, 404, "not_found", "ingestion not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if (state != "STAGING" && state != "VERIFYING") || expired {
		problem(w, r, 409, "not_writable", "ingestion does not accept uploads")
		return
	}
	var size int64
	e = tx.QueryRow(ctx, `SELECT size_bytes FROM files WHERE id=$1 AND ingestion_id=$2`, fid, ing).Scan(&size)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, r, 404, "not_found", "file not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	var aid, uid, ast string
	e = tx.QueryRow(ctx, `SELECT id,COALESCE(upload_id,''),state FROM attempts WHERE file_id=$1 AND state IN ('CREATING','ACTIVE','VERIFIED')`, fid).Scan(&aid, &uid, &ast)
	if e == nil {
		if uid == "" {
			problem(w, r, 503, "creating", "upload creation pending")
			return
		}
		output(w, map[string]string{"attempt_id": aid, "upload_id": uid, "url": "/uploads/" + uid})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		s.internal(w, r, e)
		return
	}
	aid = uuid.NewString()
	key := "uploads/" + aid
	_, e = tx.Exec(ctx, `INSERT INTO attempts(id,file_id,object_key,state) VALUES($1,$2,$3,'CREATING')`, aid, fid, key)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	fault.Hit("attempt_reserved")
	creationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	upload, e := s.Store.NewUpload(creationCtx, tus.FileInfo{ID: aid, Size: size})
	if e != nil {
		s.DB.Exec(creationCtx, `UPDATE attempts SET state='TOMBSTONED',error='creation failed' WHERE id=$1 AND state='CREATING'`, aid)
		s.internal(w, r, e)
		return
	}
	info, e := upload.GetInfo(creationCtx)
	if e == nil && size == 0 {
		e = upload.FinishUpload(creationCtx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	fault.Hit("attempt_storage_created")
	tx, e = s.DB.Begin(creationCtx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(creationCtx)
	e = tx.QueryRow(creationCtx, `SELECT state,expires_at<=clock_timestamp() FROM ingestions WHERE id=$1 FOR UPDATE`, ing).Scan(&state, &expired)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if expired || (state != "STAGING" && state != "VERIFYING") {
		tx.Exec(creationCtx, `UPDATE attempts SET state='TOMBSTONED' WHERE id=$1`, aid)
		tx.Commit(creationCtx)
		problem(w, r, 409, "not_writable", "ingestion expired during creation")
		return
	}
	tag, e := tx.Exec(creationCtx, `UPDATE attempts SET upload_id=$2,state='ACTIVE' WHERE id=$1 AND state='CREATING'`, aid, info.ID)
	if e != nil || tag.RowsAffected() != 1 {
		s.internal(w, r, fmt.Errorf("creation promotion failed: %v", e))
		return
	}
	if e = tx.Commit(creationCtx); e != nil {
		s.internal(w, r, e)
		return
	}
	fault.Hit("attempt_committed")
	output(w, map[string]string{"attempt_id": aid, "upload_id": info.ID, "url": "/uploads/" + info.ID})
	s.signal()
}
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "HEAD" && r.Method != "PATCH" {
		problem(w, r, 405, "method_not_allowed", "only HEAD and PATCH are enabled")
		return
	}
	uid := r.PathValue("upload_id")
	var aid string
	e := s.DB.QueryRow(r.Context(), `SELECT a.id FROM attempts a JOIN files f ON f.id=a.file_id JOIN ingestions i ON i.id=f.ingestion_id WHERE a.upload_id=$1 AND i.project=$2`, uid, ident(r).Project).Scan(&aid)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, r, 404, "not_found", "upload not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	gate := s.gate(aid)
	gate.RLock()
	defer gate.RUnlock()
	var tokenOK bool
	e = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tokens WHERE hash=$1 AND project=$2 AND NOT revoked AND ($3=false OR access='write'))`, manifest.Digest([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))), ident(r).Project, r.Method == "PATCH").Scan(&tokenOK)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if !tokenOK {
		problem(w, r, 401, "unauthorized", "token was revoked or changed while request waited")
		return
	}
	var active bool
	e = s.DB.QueryRow(r.Context(), `SELECT a.state='ACTIVE' AND i.state IN ('STAGING','VERIFYING') AND i.expires_at>clock_timestamp() FROM attempts a JOIN files f ON f.id=a.file_id JOIN ingestions i ON i.id=f.ingestion_id WHERE a.id=$1`, aid).Scan(&active)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if !active {
		problem(w, r, 409, "not_writable", "upload is verified, expired or retired")
		return
	}
	if r.Method == "PATCH" {
		if r.ContentLength < 0 || r.ContentLength > manifest.PatchSize {
			problem(w, r, 413, "patch_limit", "PATCH requires a known length of at most 8 MiB")
			return
		}
		select {
		case s.uploads <- struct{}{}:
			defer func() { <-s.uploads }()
		case <-r.Context().Done():
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, manifest.PatchSize)
	}
	http.StripPrefix("/uploads/", s.Tus).ServeHTTP(w, r)
	s.signal()
}
func (s *Server) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Server) finalize(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tag, e := s.DB.Exec(r.Context(), `WITH changed AS (UPDATE ingestions SET finalize_requested=true,state=CASE WHEN state='STAGING' THEN 'VERIFYING' ELSE state END WHERE id=$1 AND project=$2 AND state IN ('STAGING','VERIFYING','READY') AND (expires_at>clock_timestamp() OR state='READY') RETURNING id,state) INSERT INTO events(ingestion_id,kind) SELECT id,'finalize_requested' FROM changed`, id, ident(r).Project)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, r, 409, "not_finalizable", "ingestion is missing, failed or expired")
		return
	}
	s.signal()
	s.respondState(w, r, id, false)
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("project") != ident(r).Project {
		problem(w, r, 403, "forbidden", "project mismatch")
		return
	}
	rows, e := s.DB.Query(r.Context(), `SELECT revision_id,name,digest,published_at FROM ingestions WHERE project=$1 AND state='READY' ORDER BY published_at DESC`, ident(r).Project)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, digest string
		var t time.Time
		if e = rows.Scan(&id, &name, &digest, &t); e != nil {
			s.internal(w, r, e)
			return
		}
		out = append(out, map[string]any{"revision_id": id, "name": name, "manifest_sha256": digest, "published_at": t})
	}
	if e = rows.Err(); e != nil {
		s.internal(w, r, e)
		return
	}
	output(w, out)
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	var key, version string
	var size int64
	e := s.DB.QueryRow(r.Context(), `SELECT f.object_key,f.version_id,f.size_bytes FROM files f JOIN ingestions i ON i.id=f.ingestion_id WHERE i.revision_id=$1 AND f.id=$2 AND i.project=$3 AND i.state='READY'`, r.PathValue("id"), r.PathValue("file_id"), ident(r).Project).Scan(&key, &version, &size)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, r, 404, "not_found", "published file not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	obj, e := s.S3.GetObject(r.Context(), &s3.GetObjectInput{Bucket: aws.String(s.C.Bucket), Key: aws.String(key), VersionId: aws.String(version)})
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer obj.Body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(size))
	if _, e = io.Copy(w, obj.Body); e != nil {
		slog.Warn("download_interrupted", "error", e)
	}
}

// VerifyVersion is shared by the online verifier and offline restore validation.
func (s *Server) VerifyVersion(ctx context.Context, key, version, expected string, size int64) error {
	if key == "" || version == "" || version == "null" {
		return errors.New("exact object key and version required for verification")
	}
	obj, e := s.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.C.Bucket), Key: aws.String(key), VersionId: aws.String(version)})
	if e != nil {
		return e
	}
	defer obj.Body.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(obj.Body, size+1))
	if e != nil {
		return e
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != expected {
		return ErrIntegrity
	}
	return nil
}

var ErrIntegrity = errors.New("object size or SHA-256 mismatch")
