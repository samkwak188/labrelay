package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/samkwak188/labrelay/internal/manifest"
	"github.com/samkwak188/labrelay/internal/server"
)

var version = "0.1.0-dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if e := run(); e != nil {
		slog.Error("fatal", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := server.EnvConfig()
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if cmd == "version" {
		fmt.Println(version)
		return nil
	}
	if cmd == "init-storage" {
		return server.InitStorage(ctx, c)
	}
	if cmd == "migrate" || cmd == "token" || cmd == "revoke" || cmd == "maintenance" {
		p, e := server.Pool(ctx, c)
		if e != nil {
			return e
		}
		defer p.Close()
		switch cmd {
		case "migrate":
			return server.Migrate(ctx, p)
		case "token":
			if len(os.Args) != 4 {
				return fmt.Errorf("usage: labrelayd token PROJECT read|write")
			}
			t, e := server.Token(ctx, p, os.Args[2], os.Args[3], os.Getenv("LABRELAY_BOOTSTRAP_TOKEN"))
			if e == nil {
				fmt.Println(t)
			}
			return e
		case "revoke":
			t := os.Getenv("LABRELAY_TOKEN")
			if t == "" {
				return fmt.Errorf("LABRELAY_TOKEN required")
			}
			_, e = p.Exec(ctx, `UPDATE tokens SET revoked=true WHERE hash=$1`, manifest.Digest([]byte(t)))
			return e
		case "maintenance":
			_, e = p.Exec(ctx, `UPDATE settings SET value='true' WHERE key='maintenance'`)
			return e
		}
	}
	s, e := server.New(ctx, c)
	if e != nil {
		return e
	}
	defer func() { stop(); s.Close() }()
	if cmd == "audit-storage" {
		return s.AuditStorage(ctx, os.Stdout)
	}
	if cmd == "validate-restore" {
		return s.ValidateRestore(ctx)
	}
	if cmd == "reconcile" {
		return s.Reconcile(ctx)
	}
	if cmd != "serve" {
		return fmt.Errorf("unknown command %s", cmd)
	}
	addr := os.Getenv("LABRELAY_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	h := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go s.Run(ctx)
	errs := make(chan error, 1)
	go func() { errs <- h.ListenAndServe() }()
	json.NewEncoder(os.Stderr).Encode(map[string]string{"event": "listening", "address": addr})
	select {
	case <-s.Stopped:
		c, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		h.Shutdown(c)
		return fmt.Errorf("exclusive catalog connection lost; restart required")
	case <-ctx.Done():
		c, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		return h.Shutdown(c)
	case e = <-errs:
		if e == http.ErrServerClosed {
			return nil
		}
		return e
	}
}
