// Package backup creates, verifies and restores LightMail backups.
//
// A backup is a gzip-compressed tar archive containing:
//   - manifest.json: format, versions, encryption key fingerprint, checksums
//   - lightmail.db: consistent SQLite snapshot (VACUUM INTO, safe while running)
//   - blobs/...: uploads, queued messages and (optionally) the attachment cache
//
// The master encryption key is NOT included: OAuth tokens and passwords in
// the database are unreadable without it. Store the key separately.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"lightmail/internal/blobs"
	"lightmail/internal/crypto"
	"lightmail/internal/store"
)

const Format = "lightmail-backup-v1"

type FileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format        string      `json:"format"`
	CreatedAt     time.Time   `json:"createdAt"`
	AppVersion    string      `json:"appVersion"`
	SchemaVersion int         `json:"schemaVersion"`
	KeyID         string      `json:"keyId"`
	IncludesCache bool        `json:"includesCache"`
	Files         []FileEntry `json:"files"`
}

// Create writes a backup archive to out.
func Create(ctx context.Context, db *store.DB, bs *blobs.Store, keyID, appVersion, out string, includeCache bool) (*Manifest, error) {
	tmpDir, err := os.MkdirTemp(filepath.Dir(out), ".backup-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	snap := filepath.Join(tmpDir, "lightmail.db")
	if err := db.VacuumInto(ctx, snap); err != nil {
		return nil, fmt.Errorf("database snapshot: %w", err)
	}
	ver, err := db.SchemaVersion(ctx)
	if err != nil {
		return nil, err
	}
	// Blobs that must be kept: uploads and outgoing messages, plus the
	// attachment cache on request.
	keep := map[string]bool{}
	rows, err := db.R.QueryContext(ctx, `SELECT blob_sha256 FROM uploads UNION SELECT raw_sha256 FROM outbox WHERE status NOT IN ('sent','canceled') OR copied_to_sent=0`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			keep[s] = true
		}
	}
	rows.Close()

	f, err := os.OpenFile(out+".partial", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer os.Remove(out + ".partial")
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	m := &Manifest{Format: Format, CreatedAt: time.Now().UTC(), AppVersion: appVersion, SchemaVersion: ver, KeyID: keyID, IncludesCache: includeCache}

	addFile := func(name, src string) error {
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		st, err := in.Stat()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.Copy(io.MultiWriter(tw, h), in); err != nil {
			return err
		}
		m.Files = append(m.Files, FileEntry{Name: name, Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil))})
		return nil
	}
	if err := addFile("lightmail.db", snap); err != nil {
		f.Close()
		return nil, err
	}
	err = bs.Walk(func(sha string, size int64) error {
		if !includeCache && !keep[sha] {
			return nil
		}
		return addFile("blobs/"+sha[:2]+"/"+sha, filepath.Join(bs.Dir(), sha[:2], sha))
	})
	if err != nil {
		f.Close()
		return nil, err
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(mb)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := tw.Write(mb); err != nil {
		f.Close()
		return nil, err
	}
	if err := tw.Close(); err != nil {
		f.Close()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return m, os.Rename(out+".partial", out)
}

// Report is the outcome of a verification.
type Report struct {
	Manifest    *Manifest `json:"manifest"`
	ChecksumsOK bool      `json:"checksumsOk"`
	IntegrityOK bool      `json:"integrityOk"`
	KeyMatches  *bool     `json:"keyMatches,omitempty"`
	Accounts    int       `json:"accounts"`
	Messages    int       `json:"messages"`
	Problems    []string  `json:"problems,omitempty"`
}

func (r *Report) OK() bool {
	return r.ChecksumsOK && r.IntegrityOK && (r.KeyMatches == nil || *r.KeyMatches) && len(r.Problems) == 0
}

// safeName rejects archive entries escaping the extraction directory.
func safeName(name string) (string, bool) {
	clean := path.Clean(name)
	if clean != name || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") || strings.Contains(clean, `\`) {
		return "", false
	}
	if clean == "lightmail.db" || clean == "manifest.json" {
		return clean, true
	}
	parts := strings.Split(clean, "/")
	if len(parts) == 3 && parts[0] == "blobs" && len(parts[1]) == 2 && len(parts[2]) == 64 && strings.HasPrefix(parts[2], parts[1]) {
		return clean, true
	}
	return "", false
}

// extract unpacks the archive into dir and verifies checksums.
func extract(archive, dir string) (*Manifest, []string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	tr := tar.NewReader(gz)
	sums := map[string]string{}
	var manifest *Manifest
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if h.Typeflag != tar.TypeReg {
			return nil, nil, fmt.Errorf("unexpected entry type for %q", h.Name)
		}
		name, ok := safeName(h.Name)
		if !ok {
			return nil, nil, fmt.Errorf("unsafe entry name %q", h.Name)
		}
		if name == "manifest.json" {
			b, err := io.ReadAll(io.LimitReader(tr, 10<<20))
			if err != nil {
				return nil, nil, err
			}
			manifest = &Manifest{}
			if err := json.Unmarshal(b, manifest); err != nil {
				return nil, nil, fmt.Errorf("manifest: %w", err)
			}
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, err
		}
		hs := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, hs), tr)
		out.Close()
		if err != nil {
			return nil, nil, err
		}
		sums[name] = hex.EncodeToString(hs.Sum(nil))
	}
	if manifest == nil || manifest.Format != Format {
		return nil, nil, errors.New("missing or unsupported manifest")
	}
	var problems []string
	for _, fe := range manifest.Files {
		if sums[fe.Name] != fe.SHA256 {
			problems = append(problems, "checksum mismatch: "+fe.Name)
		}
		delete(sums, fe.Name)
	}
	for name := range sums {
		problems = append(problems, "unexpected file: "+name)
	}
	return manifest, problems, nil
}

// Verify extracts to a temporary directory and checks the archive. When kr
// is set, it also checks that the key can decrypt the stored secrets.
func Verify(ctx context.Context, archive string, kr *crypto.Keyring) (*Report, error) {
	// Extract next to the archive: /tmp is a small tmpfs in the container.
	dir, err := os.MkdirTemp(filepath.Dir(archive), ".verify-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	return verifyIn(ctx, archive, dir, kr)
}

func verifyIn(ctx context.Context, archive, dir string, kr *crypto.Keyring) (*Report, error) {
	m, problems, err := extract(archive, dir)
	if err != nil {
		return nil, err
	}
	rep := &Report{Manifest: m, ChecksumsOK: len(problems) == 0, Problems: problems}
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(dir, "lightmail.db"))+"?_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err == nil && res == "ok" {
		rep.IntegrityOK = true
	} else {
		rep.Problems = append(rep.Problems, "database integrity check failed: "+res)
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&rep.Accounts)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&rep.Messages)
	if kr != nil {
		var check string
		ok := false
		if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='key_check'`).Scan(&check); err == nil {
			_, derr := kr.Decrypt(check, "meta:key_check")
			ok = derr == nil
		} else if errors.Is(err, sql.ErrNoRows) {
			ok = true
		}
		rep.KeyMatches = &ok
		if !ok {
			rep.Problems = append(rep.Problems, "the configured master key cannot decrypt this backup (key "+m.KeyID+" required)")
		}
	}
	return rep, nil
}

// Restore replaces the database and blobs in dataDir with the backup.
// The server must be stopped (the caller holds the data directory lock).
// Existing files are kept in dataDir/pre-restore-<timestamp>/.
func Restore(ctx context.Context, archive, dbPath, blobDir string, kr *crypto.Keyring) (*Report, error) {
	stage, err := os.MkdirTemp(filepath.Dir(dbPath), ".restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	rep, err := verifyIn(ctx, archive, stage, kr)
	if err != nil {
		return nil, err
	}
	if !rep.OK() {
		return rep, errors.New("backup verification failed, nothing was restored")
	}
	if rep.Manifest.SchemaVersion > store.LatestSchema() {
		return rep, fmt.Errorf("backup schema v%d is newer than this LightMail (v%d): upgrade first", rep.Manifest.SchemaVersion, store.LatestSchema())
	}
	keepDir := filepath.Join(filepath.Dir(dbPath), "pre-restore-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(keepDir, 0o700); err != nil {
		return rep, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			if err := os.Rename(dbPath+suffix, filepath.Join(keepDir, filepath.Base(dbPath)+suffix)); err != nil {
				return rep, err
			}
		}
	}
	if err := os.Rename(filepath.Join(stage, "lightmail.db"), dbPath); err != nil {
		return rep, err
	}
	srcBlobs := filepath.Join(stage, "blobs")
	err = filepath.WalkDir(srcBlobs, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(srcBlobs, p)
		dst := filepath.Join(blobDir, rel)
		if _, err := os.Stat(dst); err == nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return os.Rename(p, dst)
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rep, err
	}
	return rep, nil
}
