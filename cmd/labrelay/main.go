//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/samkwak188/labrelay/internal/client"
	"github.com/samkwak188/labrelay/internal/manifest"
	"github.com/samkwak188/labrelay/internal/server"
	"github.com/samkwak188/labrelay/internal/spool"
	"golang.org/x/sys/unix"
)

var version = "0.1.0-dev"

func emit(v any) { json.NewEncoder(os.Stdout).Encode(v) }
func main() {
	if e := run(); e != nil {
		json.NewEncoder(os.Stderr).Encode(map[string]string{"error": e.Error()})
		var a *client.APIError
		if errors.As(e, &a) && a.Body.Retryable {
			os.Exit(75)
		}
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: labrelay doctor|prepare|import cats|sync|status|list|fetch|prune")
	}
	args := os.Args[2:]
	clean := []string{}
	for _, a := range args {
		if a != "--json" {
			clean = append(clean, a)
		}
	}
	args = clean
	cmd := os.Args[1]
	if cmd == "version" {
		emit(map[string]string{"version": version})
		return nil
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	dir := os.Getenv("LABRELAY_SPOOL")
	if dir == "" {
		dir = filepath.Join(home, ".local", "state", "labrelay")
	}
	s, e := spool.Open(dir)
	if e != nil {
		return e
	}
	defer s.Close()
	connect := func() (*client.Client, error) {
		u := os.Getenv("LABRELAY_SERVER")
		if u == "" {
			u = "http://127.0.0.1:8080"
		}
		p := os.Getenv("LABRELAY_PROJECT")
		if p == "" {
			p = "pilot"
		}
		return client.New(u, p, os.Getenv("LABRELAY_TOKEN"))
	}
	switch cmd {
	case "prepare", "import":
		cats := cmd == "import"
		if cats {
			if len(args) == 0 || args[0] != "cats" {
				return fmt.Errorf("usage: import cats --root DIR --scene FILE --frames FILE")
			}
			args = args[1:]
		}
		f := flag.NewFlagSet(cmd, flag.ContinueOnError)
		root := f.String("root", "", "completed source directory")
		name := f.String("name", "", "dataset name")
		scene := f.String("scene", "", "scene card path")
		frames := f.String("frames", "", "frame manifest path")
		dry := f.Bool("dry-run", false, "validate and estimate space")
		if e = f.Parse(args); e != nil {
			return e
		}
		if *root == "" {
			return fmt.Errorf("--root is required")
		}
		var sel manifest.Selection
		if cats {
			sel, e = manifest.CATS(*root, *scene, *frames)
		} else {
			sel, e = spool.Generic(*root, *name)
		}
		if e != nil {
			return e
		}
		id, n, e := s.Prepare(ctx, *root, sel, *dry)
		if e != nil {
			return e
		}
		emit(map[string]any{"bundle_id": id, "bytes": n, "files": len(sel.Files), "dry_run": *dry})
		return nil
	case "status":
		bs, e := s.Bundles()
		if e != nil {
			return e
		}
		if len(args) > 1 {
			return fmt.Errorf("status accepts one bundle id")
		}
		if len(args) == 1 {
			for _, b := range bs {
				if b.ID == args[0] {
					out := map[string]any{"local": b}
					if b.Ingestion != "" {
						if c, e := connect(); e == nil {
							var remote server.State
							if e = c.JSON(ctx, "GET", "/v1/ingestions/"+b.Ingestion, nil, &remote); e == nil {
								out["remote"] = remote
							} else {
								out["remote_error"] = e.Error()
							}
						}
					}
					emit(out)
					return nil
				}
			}
			return fmt.Errorf("bundle not found")
		}
		emit(bs)
		return nil
	case "prune":
		n, e := s.Prune()
		if e == nil {
			emit(map[string]int{"removed_snapshots": n})
		}
		return e
	case "sync":
		watch, retry := false, false
		id := ""
		for _, a := range args {
			switch a {
			case "--watch":
				watch = true
			case "--retry":
				retry = true
			default:
				if id != "" {
					return fmt.Errorf("sync accepts one bundle id")
				}
				id = a
			}
		}
		unlock, e := s.Lock("sync")
		if e != nil {
			return e
		}
		defer unlock()
		c, e := connect()
		if e != nil {
			return e
		}
		if e = s.BindTarget(c.Base.String(), c.Project); e != nil {
			return e
		}
		for {
			bs, e := s.Bundles()
			if e != nil {
				return e
			}
			found := id == ""
			for _, b := range bs {
				if id != "" && b.ID != id {
					continue
				}
				found = true
				if b.State == "PUBLISHED" || b.State == "PRUNED" {
					continue
				}
				if e = c.Sync(ctx, s, b.ID, retry); e != nil {
					if !watch {
						return e
					}
					var a *client.APIError
					if (errors.As(e, &a) && !a.Body.Retryable) || errors.Is(e, server.ErrIntegrity) {
						return e
					}
					fmt.Fprintln(os.Stderr, e)
				}
			}
			if !found {
				return fmt.Errorf("bundle not found")
			}
			if !watch {
				seconds := float64(0)
				latency := float64(0)
				if c.UploadStart.Load() > 0 {
					seconds = float64(c.UploadEnd.Load()-c.UploadStart.Load()) / 1e9
					latency = float64(time.Since(c.Started).Nanoseconds()-c.UploadEnd.Load()) / 1e9
				}
				emit(map[string]any{"synced": true, "bytes_sent": c.BytesSent.Load(), "upload_window_seconds": seconds, "after_upload_seconds": latency})
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	case "list":
		c, e := connect()
		if e != nil {
			return e
		}
		var data any
		e = c.JSON(ctx, "GET", "/v1/projects/"+c.Project+"/revisions", nil, &data)
		if e == nil {
			emit(data)
		}
		return e
	case "fetch":
		if len(args) < 1 {
			return fmt.Errorf("fetch REVISION_ID --dest DIR")
		}
		f := flag.NewFlagSet("fetch", flag.ContinueOnError)
		dest := f.String("dest", "", "new destination directory")
		if e = f.Parse(args[1:]); e != nil {
			return e
		}
		if *dest == "" {
			return fmt.Errorf("--dest required")
		}
		c, e := connect()
		if e != nil {
			return e
		}
		if e = c.Fetch(ctx, args[0], *dest); e == nil {
			emit(map[string]any{"verified": true, "destination": *dest})
		}
		return e
	case "doctor":
		var disk unix.Statfs_t
		if e = unix.Statfs(s.Dir, &disk); e != nil {
			return e
		}
		free := disk.Bavail * uint64(disk.Bsize)
		if free < 1<<30 {
			return fmt.Errorf("spool filesystem has less than the 1 GiB safety reserve")
		}
		c, e := connect()
		if e != nil {
			return e
		}
		var live any
		if e = c.JSON(ctx, "GET", "/health/ready", nil, &live); e != nil {
			return e
		}
		var revisions any
		if e = c.JSON(ctx, "GET", "/v1/projects/"+c.Project+"/revisions", nil, &revisions); e != nil {
			return e
		}
		emit(map[string]any{"ready": true, "spool": s.Dir, "free_bytes": free, "spool_quota_bytes": s.Quota, "platform": "linux", "server": c.Base.String(), "project": c.Project})
		return nil
	default:
		return fmt.Errorf("unknown command %s", cmd)
	}
}
