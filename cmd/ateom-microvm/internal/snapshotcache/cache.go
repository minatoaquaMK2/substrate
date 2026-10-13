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

// Package snapshotcache owns immutable memory images for private mmap restores.
// Its directory must be writable only by the trusted runtime. Hardlink leases
// survive runtime crashes, and GC never truncates or rewrites a published inode.
package snapshotcache

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	memoryFile = "memory-ranges"
	// Config and device state must not turn admission into an unbounded copy.
	maxMetadataSize = 16 << 20
)

type Cache struct{ root string }

// Lease owns a private restore view. Release only after the VMM and its device
// backends have stopped; on uncertain shutdown retain the lease for recovery.
type Lease struct {
	Dir      string
	Digest   string
	mu       sync.Mutex
	released bool
}

// Open requires a runtime-owned, private directory on a filesystem supporting
// hardlinks and flock. Callers must not expose it to guests or external writers.
func Open(root string) (*Cache, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("snapshot cache directory must be absolute")
	}
	for _, p := range []string{root, filepath.Join(root, "objects"), filepath.Join(root, "leases")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, err
		}
		st, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() || st.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("cache path %q must be a private directory", p)
		}
		fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		var stat unix.Stat_t
		err = unix.Fstat(fd, &stat)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		if stat.Uid != uint32(os.Geteuid()) {
			return nil, fmt.Errorf("cache path %q is not owned by this runtime", p)
		}
	}
	return &Cache{root: root}, nil
}

func openRegular(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	return f, nil
}

// sourceInput keeps a descriptor and version from before any snapshot bytes are
// read. Final validation checks both the descriptor and its name, so replacing
// a staged file cannot silently combine memory and metadata generations.
type sourceInput struct {
	file    *os.File
	version unix.Stat_t
}

func fileVersion(f *os.File) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &st)
	return st, err
}

func sameVersion(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size &&
		a.Mtim == b.Mtim && a.Ctim == b.Ctim && a.Mode == b.Mode
}

func (s sourceInput) unchanged() error {
	current, err := fileVersion(s.file)
	if err != nil {
		return err
	}
	var named unix.Stat_t
	if err := unix.Lstat(s.file.Name(), &named); err != nil {
		return fmt.Errorf("snapshot source changed during admission: %w", err)
	}
	if !sameVersion(s.version, current) || !sameVersion(s.version, named) {
		return fmt.Errorf("snapshot source %q changed during admission", s.file.Name())
	}
	return nil
}

// lock coordinates publishers, lease acquisition and GC across ateom processes.
func (c *Cache) lock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(c.root, ".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = unix.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// copyMemory hashes logical bytes (including holes) and preserves zero chunks as
// holes. This intentionally pays one full read at admission; it is not a claim
// that integrity verification is free or that filesystem reflinks share cache.
func copyMemory(ctx context.Context, src *os.File, dst *os.File) (string, error) {
	before, err := fileVersion(src)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			size += int64(n)
			if size > before.Size {
				return "", fmt.Errorf("snapshot changed during admission")
			}
			zero := true
			for _, b := range buf[:n] {
				if b != 0 {
					zero = false
					break
				}
			}
			if zero {
				_, err = dst.Seek(int64(n), io.SeekCurrent)
			} else {
				var written int
				written, err = dst.Write(buf[:n])
				if err == nil && written != n {
					err = io.ErrShortWrite
				}
			}
			if err != nil {
				return "", err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	after, err := fileVersion(src)
	if err != nil {
		return "", err
	}
	if size != before.Size || !sameVersion(before, after) {
		return "", fmt.Errorf("snapshot changed during admission")
	}
	if err := dst.Truncate(size); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func verify(ctx context.Context, path, digest string) error {
	f, err := openRegular(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := f.Read(buf)
		_, _ = h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != digest {
		return fmt.Errorf("snapshot cache integrity mismatch for %s", digest)
	}
	return nil
}

// Acquire admits an already-authorized snapshot in a nonempty authorization
// scope. Only the memory inode is shared. CH's config/state stay private because
// restore rewrites device paths. This subset has no external vhost-user state.
func (c *Cache) Acquire(ctx context.Context, scope, sourceDir string) (_ *Lease, retErr error) {
	if strings.TrimSpace(scope) == "" {
		return nil, fmt.Errorf("snapshot authorization scope is required")
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var inputs []sourceInput
	for _, name := range []string{memoryFile, "config.json", "state.json"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := openRegular(filepath.Join(sourceDir, name))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		version, err := fileVersion(f)
		if err != nil {
			return nil, err
		}
		if name != memoryFile && version.Size > maxMetadataSize {
			return nil, fmt.Errorf("snapshot metadata %q exceeds %d bytes", name, maxMetadataSize)
		}
		inputs = append(inputs, sourceInput{file: f, version: version})
	}
	tmp, err := os.CreateTemp(filepath.Join(c.root, "objects"), ".incoming-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	digest, err := copyMemory(ctx, inputs[0].file, tmp)
	if err != nil {
		return nil, err
	}
	if err := tmp.Chmod(0o400); err != nil {
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	view, err := os.MkdirTemp(filepath.Join(c.root, "leases"), "restore-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(view)
		}
	}()
	for _, input := range inputs[1:] {
		if err := copyRegular(ctx, input.file, filepath.Join(view, filepath.Base(input.file.Name()))); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, input := range inputs {
		if err := input.unchanged(); err != nil {
			return nil, err
		}
	}
	// Validate before touching the published inode: an existing lease may be
	// our input, and retaining/linking its object changes its mtime or ctime.
	key := fmt.Sprintf("%x-%s", sha256.Sum256([]byte(scope)), digest)
	object := filepath.Join(c.root, "objects", key)
	if err := unix.Renameat2(unix.AT_FDCWD, tmp.Name(), unix.AT_FDCWD, object, unix.RENAME_NOREPLACE); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := verify(ctx, object, digest); err != nil {
			return nil, err
		}
	} else if err := syncDir(filepath.Dir(object)); err != nil {
		return nil, err
	}
	// Refresh retention metadata without changing the published memory bytes.
	if err := os.Chtimes(object, time.Now(), time.Now()); err != nil {
		return nil, err
	}
	if err := os.Link(object, filepath.Join(view, memoryFile)); err != nil {
		return nil, err
	}
	if err := syncDir(view); err != nil {
		return nil, err
	}
	if err := syncDir(filepath.Dir(view)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Lease{Dir: view, Digest: digest}, nil
}

type contextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

func copyRegular(ctx context.Context, in *os.File, dst string) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(contextReader{ctx: ctx, in: in}, maxMetadataSize+1))
	if err != nil {
		return err
	}
	if n > maxMetadataSize {
		return fmt.Errorf("snapshot metadata exceeds %d bytes", maxMetadataSize)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return out.Sync()
}

func (l *Lease) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	if err := os.RemoveAll(l.Dir); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(l.Dir)); err != nil {
		return err
	}
	l.released = true
	return nil
}

// GC unlinks unleased objects only. Orphaned lease directories are retained after
// a crash; their deletion requires a separate proof that their guest has stopped.
func (c *Cache) GC(ctx context.Context) (int, error) {
	return c.GCBefore(ctx, time.Now())
}

// GCBefore retains unleased objects admitted after cutoff, allowing bounded-time
// warm reuse. Live/crash leases always win over age; no process ID is trusted.
func (c *Cache) GCBefore(ctx context.Context, cutoff time.Time) (int, error) {
	unlock, err := c.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer unlock()
	entries, err := os.ReadDir(filepath.Join(c.root, "objects"))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		path := filepath.Join(c.root, "objects", entry.Name())
		if strings.HasPrefix(entry.Name(), ".incoming-") {
			// All admission I/O holds this same lock. No live producer owns
			// this staging file; it was left by a crashed admission.
			if err := os.Remove(path); err != nil {
				return removed, err
			}
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			return removed, err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG {
			return removed, fmt.Errorf("invalid cache object %q", path)
		}
		if time.Unix(st.Mtim.Sec, st.Mtim.Nsec).After(cutoff) {
			continue
		}
		if st.Nlink != 1 {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, syncDir(filepath.Join(c.root, "objects"))
}
