package oshsetup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The setup runs as root but reads and writes in the home of OpenShell's
// gateway user, which that user controls. Every access there goes through
// an os.Root opened on the home: a path is resolved inside it, so a
// symbolic link the user put there cannot lead root to a file outside
// (/etc/shadow, /etc/systemd/system). Only regular files are read, never a
// FIFO or a device; a file is written as a new one, owned by the user
// through its descriptor before it is renamed into place, never by a
// chown of a path the user could have swapped (security review 0.8,
// S1–S4).

// maxHomeFile is the largest file the setup reads from the home: its
// configuration and certificates are a few kilobytes.
const maxHomeFile = 1 << 20

// fchown and lchown give a file or directory to the user; tests replace
// them, as a test does not run as root.
var (
	fchown = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
	lchown = func(r *os.Root, name string, uid, gid int) error { return r.Lchown(name, uid, gid) }
)

// homeRel is path relative to the user's home; path must be inside it.
func (s *setup) homeRel(path string) (string, error) {
	rel, err := filepath.Rel(s.u.home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s: not in %s's home %s", path, s.u.name, s.u.home)
	}
	return rel, nil
}

func (s *setup) openHome() (*os.Root, error) {
	r, err := os.OpenRoot(s.a.Path(s.u.home))
	if err != nil {
		return nil, fmt.Errorf("%s's home: %w", s.u.name, err)
	}
	return r, nil
}

// readHome reads a regular file in the user's home.
func (s *setup) readHome(path string) ([]byte, error) {
	rel, err := s.homeRel(path)
	if err != nil {
		return nil, err
	}
	r, err := s.openHome()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := readRegular(r, rel)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, err
}

// readRegular reads name in r if it is a regular file: opened without
// waiting (a FIFO would block), then checked on the open descriptor.
func readRegular(r *os.Root, name string) ([]byte, error) {
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file (%s)", fi.Mode().Type())
	}
	b, err := io.ReadAll(io.LimitReader(f, maxHomeFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxHomeFile {
		return nil, fmt.Errorf("larger than %d bytes", maxHomeFile)
	}
	return b, nil
}

// writeHome makes a file in the user's home have content, as the user's
// file with mode; missing directories are made as the user's. It says
// whether the file changed.
func (s *setup) writeHome(path string, content []byte, mode os.FileMode) (bool, error) {
	rel, err := s.homeRel(path)
	if err != nil {
		return false, err
	}
	r, err := s.openHome()
	if err != nil {
		return false, err
	}
	defer r.Close()
	if old, err := readRegular(r, rel); err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if s.o.Check {
		return true, nil
	}
	if err := s.homeDirs(r, filepath.Dir(rel)); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	tmp := fmt.Sprintf("%s.custos-%d.tmp", rel, os.Getpid())
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	done := false
	defer func() {
		if !done {
			f.Close()
			r.Remove(tmp)
		}
	}()
	for _, step := range []func() error{
		func() error { return fchown(f, s.u.uid, s.u.gid) },
		func() error { return f.Chmod(mode) },
		func() error { _, err := f.Write(content); return err },
		f.Sync,
		f.Close,
	} {
		if err := step(); err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := r.Rename(tmp, rel); err != nil {
		r.Remove(tmp)
		done = true
		return false, fmt.Errorf("%s: %w", path, err)
	}
	done = true
	return true, nil
}

// homeDirs makes the missing directories of dir (relative to the home),
// each as the user's.
func (s *setup) homeDirs(r *os.Root, dir string) error {
	if dir == "." {
		return nil
	}
	p := ""
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		if _, err := r.Stat(p); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := r.Mkdir(p, 0o755); err != nil {
			return err
		}
		if err := lchown(r, p, s.u.uid, s.u.gid); err != nil {
			return err
		}
	}
	return nil
}

// homeFiles are the regular files below dir in the user's home, by their
// path relative to dir. Symbolic links, FIFOs and devices are left out.
func (s *setup) homeFiles(dir string) (map[string][]byte, error) {
	rel, err := s.homeRel(dir)
	if err != nil {
		return nil, err
	}
	r, err := s.openHome()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	sub, err := r.OpenRoot(rel)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	defer sub.Close()
	files := map[string][]byte{}
	err = fs.WalkDir(sub.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := readRegular(sub, p)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(dir, p), err)
		}
		files[p] = b
		return nil
	})
	return files, err
}
