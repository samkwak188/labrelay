// A measurement fixture, not a third supported application binary.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bdragon300/tusgo"
	"github.com/samkwak188/labrelay/internal/server"
	"github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
	"github.com/tus/tusd/v2/pkg/s3store"
	"net/http"
	"net/url"
	"os"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	token := os.Getenv("LABRELAY_TOKEN")
	if token == "" {
		return fmt.Errorf("LABRELAY_TOKEN required")
	}
	if len(os.Args) < 2 {
		return fmt.Errorf("server or upload FILE")
	}
	if os.Args[1] == "server" {
		cfg := server.EnvConfig()
		sc, e := server.Storage(context.Background(), cfg)
		if e != nil {
			return e
		}
		store := s3store.New(cfg.Bucket, sc)
		store.ObjectPrefix = "uploads/baseline-"
		store.PreferredPartSize = 8 << 20
		store.MaxBufferedParts = 1
		store.SetConcurrentPartUploads(4)
		store.TemporaryDirectory = cfg.Temp
		if e = os.MkdirAll(cfg.Temp, 0700); e != nil {
			return e
		}
		composer := handler.NewStoreComposer()
		store.UseIn(composer)
		memorylocker.New().UseIn(composer)
		h, e := handler.NewHandler(handler.Config{BasePath: "/uploads/", StoreComposer: composer, MaxSize: 2 << 30, DisableDownload: true, DisableTermination: true, DisableConcatenation: true, NetworkTimeout: 30 * time.Second, GracefulRequestCompletionTimeout: 10 * time.Second})
		if e != nil {
			return e
		}
		gate := make(chan struct{}, 4)
		mux := http.NewServeMux()
		mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		mux.HandleFunc("/uploads/", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "unauthorized", 401)
				return
			}
			if r.Method == "PATCH" {
				gate <- struct{}{}
				defer func() { <-gate }()
				r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
			}
			http.StripPrefix("/uploads/", h).ServeHTTP(w, r)
		})
		return http.ListenAndServe("127.0.0.1:18082", mux)
	}
	if os.Args[1] != "upload" || len(os.Args) != 3 {
		return fmt.Errorf("upload FILE")
	}
	file, e := os.Open(os.Args[2])
	if e != nil {
		return e
	}
	defer file.Close()
	fi, e := file.Stat()
	if e != nil {
		return e
	}
	base, _ := url.Parse("http://127.0.0.1:18082")
	hc := &http.Client{Transport: auth{token: token, next: http.DefaultTransport}}
	req, _ := http.NewRequest("POST", base.String()+"/uploads/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", fmt.Sprint(fi.Size()))
	resp, e := hc.Do(req)
	if e != nil {
		return e
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		return fmt.Errorf("creation: %s", resp.Status)
	}
	u := &tusgo.Upload{Location: resp.Header.Get("Location"), RemoteSize: fi.Size()}
	stream := tusgo.NewUploadStream(tusgo.NewClient(hc, base), u)
	stream.ChunkSize = 8 << 20
	if _, e = stream.Sync(); e != nil {
		return e
	}
	start := time.Now()
	n, e := stream.ReadFrom(file)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"bytes": n, "upload_window_seconds": time.Since(start).Seconds()})
}

type auth struct {
	token string
	next  http.RoundTripper
}

func (a auth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return a.next.RoundTrip(r)
}
