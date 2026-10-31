// Package blobs is a content-addressed file store for cached attachments,
// uploads and outgoing messages. Files are named by their SHA-256, so user
// supplied names never reach the filesystem and identical content is stored
// once.
package blobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var (
	ErrTooLarge = errors.New("content exceeds the size limit")
	ErrInvalid  = errors.New("invalid blob identifier")
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Store struct{ dir string }

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) path(sha string) (string, error) {
	if !shaPattern.MatchString(sha) {
		return "", ErrInvalid
	}
	return filepath.Join(s.dir, sha[:2], sha), nil
}

// Put stores r (at most limit bytes when limit > 0) and returns its hash.
func (s *Store) Put(r io.Reader, limit int64) (sha string, size int64, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "tmp"), "up-*")
	if err != nil {
		return "", 0, err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	src := r
	if limit > 0 {
		src = io.LimitReader(r, limit+1)
	}
	size, err = io.Copy(io.MultiWriter(tmp, h), src)
	if err != nil {
		return "", 0, err
	}
	if limit > 0 && size > limit {
		return "", 0, ErrTooLarge
	}
	if err = tmp.Sync(); err != nil {
		return "", 0, err
	}
	if err = tmp.Close(); err != nil {
		return "", 0, err
	}
	sha = hex.EncodeToString(h.Sum(nil))
	dst, _ := s.path(sha)
	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, err
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		os.Remove(tmp.Name()) // deduplicated
		return sha, size, nil
	}
	if err = os.Rename(tmp.Name(), dst); err != nil {
		return "", 0, err
	}
	return sha, size, nil
}

func (s *Store) Open(sha string) (*os.File, error) {
	p, err := s.path(sha)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (s *Store) Exists(sha string) bool {
	p, err := s.path(sha)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func (s *Store) Delete(sha string) error {
	p, err := s.path(sha)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Walk lists stored blob hashes (for orphan cleanup and backups).
func (s *Store) Walk(fn func(sha string, size int64) error) error {
	return filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "tmp" {
			return filepath.SkipDir
		}
		if d.IsDir() || !shaPattern.MatchString(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return fn(d.Name(), info.Size())
	})
}

// CleanTmp removes leftovers of interrupted writes.
func (s *Store) CleanTmp() error {
	entries, err := os.ReadDir(filepath.Join(s.dir, "tmp"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		os.Remove(filepath.Join(s.dir, "tmp", e.Name()))
	}
	return nil
}

func (s *Store) String() string { return fmt.Sprintf("blobs(%s)", s.dir) }
