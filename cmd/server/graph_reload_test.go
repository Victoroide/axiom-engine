//go:build linux || darwin

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func replaceCurriculum(t *testing.T, path, contents string) string {
	t.Helper()
	staged := path + ".staged"
	if err := os.WriteFile(staged, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, path); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(contents)))
}

func curriculumJSON(root string) string {
	return fmt.Sprintf(`[{"subject":"Recursion","relation":"depends_on","object":%q}]`, root)
}

func TestGraphReloadKeepsLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	cfg := config.Config{GraphFile: path, GraphReloadEnabled: true}
	if _, err := loadServingCurriculum(cfg); err == nil {
		t.Fatal("startup accepted a missing initial file")
	}
	replaceCurriculum(t, path, "invalid initial curriculum")
	if _, err := loadServingCurriculum(cfg); err == nil {
		t.Fatal("startup accepted an invalid initial file")
	}
	startRevision := replaceCurriculum(t, path, curriculumJSON("Old Root"))
	curriculum, err := loadServingCurriculum(cfg)
	if err != nil {
		t.Fatal(err)
	}
	initial := curriculum.Current()
	if initial.Revision() != startRevision {
		t.Fatalf("initial revision=%q want %q", initial.Revision(), startRevision)
	}
	logs := &processRecords{messages: make(chan map[string]any, 16), shutdown: make(chan struct{})}
	r := startGraphReload(cfg, curriculum, slog.New(slog.NewJSONHandler(logs, nil)))
	defer func() {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := r.close(ctx); err != nil {
			t.Error(err)
		}
	}()
	assertGraphReloadLog(t, logs, "loaded", startRevision, 0, 0)
	failed := 0
	for _, replacement := range []string{"unchanged", "validation", "missing", "unreadable", "fifo", "directory", "oversized"} {
		t.Run(replacement, func(t *testing.T) {
			if replacement == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read files without read permission")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "unchanged":
				replaceCurriculum(t, path, curriculumJSON("Old Root"))
			case "validation":
				replaceCurriculum(t, path, `[{"subject":"private-topic","relation":"private-relation","object":"private-root"}]`)
			case "unreadable":
				replaceCurriculum(t, path, curriculumJSON("Unreadable Root"))
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				replaceCurriculum(t, path, curriculumJSON("Oversized Root"))
				if err := os.Truncate(path, 8*1024*1024+1); err != nil {
					t.Fatal(err)
				}
			}
			r.signals <- syscall.SIGHUP
			outcome := "unchanged"
			if replacement != "unchanged" {
				failed++
				outcome = "failed"
			}
			assertGraphReloadLog(t, logs, outcome, startRevision, 0, failed)
			if curriculum.Current() != initial {
				t.Fatal("replacement changed the active snapshot")
			}
			if replacement == "missing" {
				replaceCurriculum(t, path, curriculumJSON("Old Root"))
			}
		})
	}
	newRevision := replaceCurriculum(t, path, curriculumJSON("New Root"))
	r.signals <- syscall.SIGHUP
	assertGraphReloadLog(t, logs, "published", newRevision, 1, failed)
	if curriculum.Current().Revision() != newRevision || !curriculum.Current().Graph().NodeExists("New Root") || !initial.Graph().NodeExists("Old Root") {
		t.Fatal("publication did not preserve both complete snapshots")
	}
	if content := logs.text(); strings.Contains(content, "private-") || strings.Contains(content, path) {
		t.Fatalf("reload logs included curriculum contents or its path: %s", content)
	}
}

func TestGraphReloadCoalescesAndCancels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	replaceCurriculum(t, path, curriculumJSON("Old Root"))
	curriculum, err := loadServingCurriculum(config.Config{GraphFile: path, GraphReloadEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	initial := curriculum.Current()
	replaceCurriculum(t, path, curriculumJSON("New Root"))
	next, err := graph.LoadSnapshot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		r := &graphReload{curriculum: curriculum, cancel: cancel, signals: make(chan os.Signal, 1), done: make(chan struct{}), logger: slog.New(slog.DiscardHandler)}
		defer r.stop()
		first := make(chan struct{})
		defer close(first)
		calls := 0
		loads := make(chan int, 4)
		go func() {
			r.run(ctx, func(ctx context.Context, _ string) (*graph.Snapshot, error) {
				calls++
				loads <- calls
				if calls == 1 {
					select {
					case <-first:
					case <-ctx.Done():
					}
					return nil, io.ErrUnexpectedEOF
				}
				<-ctx.Done()
				// Even a valid graph returned as shutdown begins cannot publish.
				return next, nil
			})
		}()
		r.signals <- syscall.SIGHUP
		if call := <-loads; call != 1 {
			t.Fatalf("first load=%d", call)
		}
		for range 100 {
			select {
			case r.signals <- syscall.SIGHUP:
			default:
			}
		}
		if len(r.signals) != 1 {
			t.Fatal("signal burst did not coalesce to one retry")
		}
		// Release the interrupted load and wait for its sole pending retry.
		first <- struct{}{}
		if call := <-loads; call != 2 {
			t.Fatalf("pending load=%d", call)
		}
		if published, failed := r.counts(); published != 0 || failed != 1 {
			t.Fatalf("published=%d failed=%d", published, failed)
		}
		r.signals <- syscall.SIGHUP
		r.stop()
		<-r.done
		if curriculum.Current() != initial || len(loads) != 0 {
			t.Fatal("shutdown published a replacement or started the pending load")
		}
	})
}

func TestGraphReloadCloseWaitsForCleanup(t *testing.T) {
	for _, expire := range []bool{false, true} {
		name := "cleanup"
		if expire {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "curriculum-*.json")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				logs := &processRecords{}
				r := &graphReload{curriculum: graph.StaticCurriculum(graph.NewInMemoryGraph()),
					signals: make(chan os.Signal, 1), done: make(chan struct{}), cancel: cancel, logger: slog.New(slog.NewJSONHandler(logs, nil))}
				started, cleanup := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(cleanup) })
				defer release()
				go func() {
					r.run(ctx, func(ctx context.Context, _ string) (*graph.Snapshot, error) {
						defer func() { _ = file.Close() }()
						close(started)
						<-ctx.Done()
						<-cleanup
						return nil, ctx.Err()
					})
				}()
				r.signals <- syscall.SIGHUP
				<-started
				shutdownCtx, stop := context.WithTimeout(t.Context(), time.Second)
				defer stop()
				result := make(chan error, 1)
				go func() { result <- r.close(shutdownCtx) }()
				<-ctx.Done()
				synctest.Wait()
				select {
				case err := <-result:
					t.Fatalf("close returned before loader cleanup or shutdown deadline: %v", err)
				default:
				}
				if expire {
					<-shutdownCtx.Done()
					synctest.Wait()
					select {
					case err := <-result:
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("close after deadline: %v", err)
						}
					default:
						t.Fatal("close is still waiting after the deadline")
					}
					if _, err := file.Stat(); err != nil {
						t.Fatalf("loader released its file before the cleanup barrier: %v", err)
					}
				}
				release()
				if !expire {
					if err := <-result; err != nil {
						t.Fatalf("close after cleanup: %v", err)
					}
					if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Fatalf("close returned before the file was closed: %v", err)
					}
					if !strings.Contains(logs.text(), `"outcome":"canceled"`) {
						t.Fatal("close returned before the canceled outcome was logged")
					}
				}
				<-r.done
			})
		})
	}
}

func TestGraphReloadValidateStillOneShot(t *testing.T) {
	t.Setenv("GRAPH_RELOAD_ENABLED", "invalid")
	t.Setenv("GRAPH_FILE", "missing.json")
	path := filepath.Join(t.TempDir(), "graph.json")
	replaceCurriculum(t, path, curriculumJSON("Root"))
	var out strings.Builder
	if err := run([]string{"validate-graph", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"concepts":2`) || strings.Contains(out.String(), "revision") {
		t.Fatalf("unexpected validation output: %s", out.String())
	}
}
