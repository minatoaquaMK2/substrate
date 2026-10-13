//go:build linux

// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshotcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func source(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data := make([]byte, 2*os.Getpagesize())
	data[0] = 0x31
	for name, b := range map[string][]byte{memoryFile: data, "config.json": []byte(`{"memory":{"shared":false}}`), "state.json": []byte(`{}`)} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func cache(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func acquire(t *testing.T, c *Cache, scope, src string) *Lease {
	t.Helper()
	l, err := c.Acquire(context.Background(), scope, src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Release(); err != nil {
			t.Error(err)
		}
	})
	return l
}
func same(t *testing.T, a, b string) bool {
	t.Helper()
	sa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(sa, sb)
}
func mapped(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := unix.Mmap(int(f.Fd()), 0, 2*os.Getpagesize(), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Munmap(b); err != nil {
			t.Error(err)
		}
	})
	return b
}

func TestLeaseCoWIsolationAndScope(t *testing.T) {
	c := cache(t)
	src := source(t)
	a := acquire(t, c, "tenant-a", src)
	b := acquire(t, c, "tenant-a", src)
	other := acquire(t, c, "tenant-b", src)
	ap, bp := filepath.Join(a.Dir, memoryFile), filepath.Join(b.Dir, memoryFile)
	if !same(t, ap, bp) {
		t.Fatal("same authorized source does not share inode")
	}
	if same(t, ap, filepath.Join(other.Dir, memoryFile)) {
		t.Fatal("different scopes shared a memory inode")
	}
	if same(t, ap, filepath.Join(src, memoryFile)) {
		t.Fatal("cache aliased mutable source")
	}
	am, bm := mapped(t, ap), mapped(t, bp)
	am[0] = 0xa1
	bm[os.Getpagesize()] = 0xb2
	if bm[0] != 0x31 || am[os.Getpagesize()] != 0 {
		t.Fatal("private writes escaped clone")
	}
	golden, err := os.ReadFile(ap)
	if err != nil {
		t.Fatal(err)
	}
	if golden[0] != 0x31 || golden[os.Getpagesize()] != 0 {
		t.Fatal("private writes changed golden")
	}
	// Recheckpoint each private mapping, then repeat cache+mapping restore.
	for _, mem := range [][]byte{am, bm} {
		for round := 0; round < 3; round++ {
			next := source(t)
			if err := os.WriteFile(filepath.Join(next, memoryFile), mem, 0o600); err != nil {
				t.Fatal(err)
			}
			lease := acquire(t, c, "tenant-a", next)
			restored := mapped(t, filepath.Join(lease.Dir, memoryFile))
			if restored[0] != mem[0] || restored[os.Getpagesize()] != mem[os.Getpagesize()] {
				t.Fatal("recheckpoint lost private dirty bytes")
			}
			mem = restored
		}
	}
}

func TestGCAndRuntimeRestartPreserveLeases(t *testing.T) {
	c := cache(t)
	src := source(t)
	lease := acquire(t, c, "scope", src)
	// A fresh Cache carries no in-memory reference count. Its GC still sees the
	// hardlink lease, modeling a service crash while the VMM survives.
	reopened, err := Open(c.root)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.GC(context.Background()); n != 0 || err != nil {
		t.Fatalf("GC active lease: %d %v", n, err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(lease.Dir, memoryFile)); err != nil || b[0] != 0x31 {
		t.Fatalf("deleted source damaged lease: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.GC(context.Background()); n != 1 || err != nil {
		t.Fatalf("GC released object: %d %v", n, err)
	}
}

func TestConcurrentAcquireReleaseAndGC(t *testing.T) {
	c := cache(t)
	src := source(t)
	ctx := context.Background()
	anchor := acquire(t, c, "scope", src)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 4 {
				l, err := c.Acquire(ctx, "scope", src)
				if err != nil {
					t.Error(err)
					return
				}
				if !same(t, filepath.Join(anchor.Dir, memoryFile), filepath.Join(l.Dir, memoryFile)) {
					t.Error("lost shared inode")
				}
				if _, err := c.GC(ctx); err != nil {
					t.Error(err)
				}
				if err := l.Release(); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if err := anchor.Release(); err != nil {
		t.Fatal(err)
	}
	if n, err := c.GC(ctx); n != 1 || err != nil {
		t.Fatalf("GC: %d %v", n, err)
	}
}

func TestRejectInvalidSourcesAndRollback(t *testing.T) {
	c := cache(t)
	src := source(t)
	ctx := context.Background()
	if _, err := c.Acquire(ctx, "", src); err == nil {
		t.Fatal("accepted unscoped source")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Acquire(canceled, "scope", src); err == nil {
		t.Fatal("ignored cancellation")
	}
	if err := os.Remove(filepath.Join(src, "state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(ctx, "scope", src); err == nil {
		t.Fatal("accepted missing state")
	}
	views, err := os.ReadDir(filepath.Join(c.root, "leases"))
	if err != nil || len(views) != 0 {
		t.Fatalf("failed admission leaked lease: %v %v", views, err)
	}
	if _, err := c.GC(ctx); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(src, memoryFile)
	if err := os.Rename(original, original+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original+".real", original); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(ctx, "scope", src); err == nil {
		t.Fatal("accepted symlink source")
	}
}

func TestCorruptObjectIsNeverReusedOrReplaced(t *testing.T) {
	c := cache(t)
	src := source(t)
	l := acquire(t, c, "scope", src)
	// Simulate privileged out-of-band corruption; chmod is not a defense against
	// the trusted runtime owner, so each reuse must recheck the content digest.
	path := filepath.Join(l.Dir, memoryFile)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(context.Background(), "scope", src); err == nil {
		t.Fatal("reused corrupted object")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "corrupt" {
		t.Fatalf("rewrote live inode: %q %v", b, err)
	}
}

func TestCacheRequiresPrivateDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("accepted public cache")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("accepted symlink cache")
	}
	if _, err := Open("relative"); err == nil {
		t.Fatal("accepted relative cache")
	}
}

func TestGCCleansCrashStaging(t *testing.T) {
	c := cache(t)
	ctx := context.Background()
	src := source(t)
	// Simulate both an abandoned pre-publication temporary and the extra
	// hardlink that an interrupted link-based publisher could have left.
	l := acquire(t, c, "scope", src)
	incoming := filepath.Join(c.root, "objects", ".incoming-crashed")
	if err := os.Link(filepath.Join(l.Dir, memoryFile), incoming); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.root, "objects", ".incoming-partial"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := c.GC(ctx); n != 1 || err != nil {
		t.Fatalf("crash GC: %d %v", n, err)
	}
	files, err := os.ReadDir(filepath.Join(c.root, "objects"))
	if err != nil || len(files) != 0 {
		t.Fatalf("crash staging remains: %v %v", files, err)
	}
}

func TestGCWarmRetention(t *testing.T) {
	c := cache(t)
	src := source(t)
	l := acquire(t, c, "scope", src)
	digest := l.Digest
	before, err := os.Stat(filepath.Join(l.Dir, memoryFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if n, err := c.GCBefore(context.Background(), time.Now().Add(-time.Hour)); n != 0 || err != nil {
		t.Fatalf("warm GC: %d %v", n, err)
	}
	next := acquire(t, c, "scope", src)
	after, err := os.Stat(filepath.Join(next.Dir, memoryFile))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || next.Digest != digest {
		t.Fatal("warm admission did not reuse immutable inode")
	}
}

// admissionContext makes source changes at an I/O boundary deterministic without
// hooks in the cache. It is only used by these synchronous admission tests.
type admissionContext struct {
	context.Context
	check func() error
}

func (c admissionContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	return c.check()
}

func TestAcquireRejectsFIFOWithoutBlocking(t *testing.T) {
	for _, name := range []string{memoryFile, "config.json", "state.json"} {
		t.Run(name, func(t *testing.T) {
			c, src := cache(t), source(t)
			path := filepath.Join(src, name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				lease, err := c.Acquire(ctx, "scope", src)
				if lease != nil {
					_ = lease.Release()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("accepted FIFO source")
				}
			case <-time.After(time.Second):
				// Unblock a regressed O_RDONLY open before failing, so the test
				// cannot leave a goroutine holding the admission lock.
				fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd)
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("admission did not exit after FIFO was unblocked")
				}
				t.Fatal("FIFO open blocked past context deadline")
			}
		})
	}
}

func TestAcquireRejectsSourceMutation(t *testing.T) {
	for _, name := range []string{memoryFile, "config.json", "state.json"} {
		for _, change := range []string{"replace", "rewrite-preserving-mtime", "truncate", "grow"} {
			t.Run(name+"/"+change, func(t *testing.T) {
				c, src := cache(t), source(t)
				path := filepath.Join(src, name)
				before, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				mutated := false
				ctx := admissionContext{Context: context.Background(), check: func() error {
					entries, err := os.ReadDir(filepath.Join(c.root, "objects"))
					if err != nil {
						t.Fatal(err)
					}
					if mutated || len(entries) == 0 {
						return nil
					}
					mutated = true
					data := []byte(strings.Repeat("x", int(before.Size())))
					switch change {
					case "replace":
						if err := os.WriteFile(path+".replacement", data, 0o600); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(path+".replacement", path); err != nil {
							t.Fatal(err)
						}
					case "rewrite-preserving-mtime":
						if err := os.WriteFile(path, data, 0o600); err != nil {
							t.Fatal(err)
						}
					case "truncate":
						if err := os.Truncate(path, before.Size()-1); err != nil {
							t.Fatal(err)
						}
					case "grow":
						if err := os.Truncate(path, before.Size()+1); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
					return nil
				}}
				lease, err := c.Acquire(ctx, "scope", src)
				if lease != nil {
					_ = lease.Release()
				}
				if !mutated {
					t.Fatal("source mutation was not exercised")
				}
				if err == nil {
					t.Fatal("admitted changed source")
				}
				views, err := os.ReadDir(filepath.Join(c.root, "leases"))
				if err != nil || len(views) != 0 {
					t.Fatalf("failed admission leaked lease: %v %v", views, err)
				}
			})
		}
	}
}

func TestAcquireMetadataBoundAndCancellation(t *testing.T) {
	for _, name := range []string{"config.json", "state.json"} {
		t.Run(name+"/oversized", func(t *testing.T) {
			c, src := cache(t), source(t)
			// Sparse input proves the bound without allocating a large fixture.
			if err := os.Truncate(filepath.Join(src, name), (16<<20)+1); err != nil {
				t.Fatal(err)
			}
			lease, err := c.Acquire(context.Background(), "scope", src)
			if lease != nil {
				_ = lease.Release()
			}
			if err == nil {
				t.Fatal("accepted oversized metadata")
			}
		})
	}
	t.Run("canceled-during-metadata-copy", func(t *testing.T) {
		c, src := cache(t), source(t)
		if err := os.Truncate(filepath.Join(src, "config.json"), 2<<20); err != nil {
			t.Fatal(err)
		}
		canceled := false
		ctx := admissionContext{Context: context.Background(), check: func() error {
			matches, err := filepath.Glob(filepath.Join(c.root, "leases", "restore-*", "config.json"))
			if err != nil {
				return fmt.Errorf("inspect metadata output: %w", err)
			}
			for _, path := range matches {
				st, err := os.Stat(path)
				if err != nil {
					return err
				}
				if st.Size() > 0 {
					canceled = true
					return context.Canceled
				}
			}
			return nil
		}}
		lease, err := c.Acquire(ctx, "scope", src)
		if lease != nil {
			_ = lease.Release()
		}
		if !canceled || !errors.Is(err, context.Canceled) {
			t.Fatalf("metadata cancellation not honored: exercised=%v error=%v", canceled, err)
		}
		views, err := os.ReadDir(filepath.Join(c.root, "leases"))
		if err != nil || len(views) != 0 {
			t.Fatalf("canceled admission leaked lease: %v %v", views, err)
		}
	})
}

func TestAcquireValidatesMetadataBeforeMemoryCopy(t *testing.T) {
	c, src := cache(t), source(t)
	if err := os.Remove(filepath.Join(src, "state.json")); err != nil {
		t.Fatal(err)
	}
	readMemory := false
	ctx := admissionContext{Context: context.Background(), check: func() error {
		entries, err := os.ReadDir(filepath.Join(c.root, "objects"))
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			readMemory = true
			return context.Canceled
		}
		return nil
	}}
	if _, err := c.Acquire(ctx, "scope", src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing pinned state before memory I/O, got %v", err)
	}
	if readMemory {
		t.Fatal("started copying memory before validating metadata sources")
	}
}

func TestAcquireFromExistingLease(t *testing.T) {
	c := cache(t)
	original := acquire(t, c, "scope", source(t))
	next := acquire(t, c, "scope", original.Dir)
	if !same(t, filepath.Join(original.Dir, memoryFile), filepath.Join(next.Dir, memoryFile)) {
		t.Fatal("lease-derived restore did not reuse the immutable memory inode")
	}
	if original.Digest != next.Digest {
		t.Fatal("lease-derived restore changed the memory digest")
	}
}
