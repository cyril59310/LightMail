package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lightmail/internal/blobs"
	"lightmail/internal/crypto"
	"lightmail/internal/store"
)

func TestBackupVerifyRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "lightmail.db")
	db, err := store.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	kr, _ := crypto.NewKeyring(bytes.Repeat([]byte{7}, 32))
	if err := db.CheckKey(ctx, kr); err != nil {
		t.Fatal(err)
	}
	u, _ := db.CreateUser(ctx, "admin", "h", true)
	a := &store.Account{UserID: u.ID, Provider: "custom", Email: "a@b.c", IMAPHost: "h", IMAPPort: 993, IMAPSecurity: "tls", SMTPHost: "h", SMTPPort: 465, SMTPSecurity: "tls", AuthType: "password", Username: "a"}
	db.CreateAccount(ctx, a)
	bs, _ := blobs.New(filepath.Join(dir, "blobs"))
	sha, size, _ := bs.Put(strings.NewReader("draft attachment"), 0)
	db.TouchBlob(ctx, sha, size)
	db.CreateUpload(ctx, &store.Upload{ID: "u1", UserID: u.ID, Filename: "f.txt", ContentType: "text/plain", Size: size, BlobSHA256: sha})
	cacheSHA, _, _ := bs.Put(strings.NewReader("cached attachment"), 0)

	out := filepath.Join(dir, "backup.tar.gz")
	m, err := Create(ctx, db, bs, kr.PrimaryID(), "test", out, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 2 { // database + referenced upload (cache excluded)
		t.Fatalf("files: %+v", m.Files)
	}
	db.Close()

	rep, err := Verify(ctx, out, kr)
	if err != nil || !rep.OK() || rep.Accounts != 1 {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	wrong, _ := crypto.NewKeyring(bytes.Repeat([]byte{8}, 32))
	rep, _ = Verify(ctx, out, wrong)
	if rep.OK() || rep.KeyMatches == nil || *rep.KeyMatches {
		t.Fatal("wrong key not detected")
	}

	// Simulate loss: remove everything, then restore.
	os.Remove(dbPath)
	os.RemoveAll(filepath.Join(dir, "blobs"))
	if _, err := Restore(ctx, out, dbPath, filepath.Join(dir, "blobs"), kr); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CheckKey(ctx, kr); err != nil {
		t.Fatal(err)
	}
	list, _ := db.ListAccounts(ctx, 0)
	if len(list) != 1 {
		t.Fatal("account not restored")
	}
	bs2, _ := blobs.New(filepath.Join(dir, "blobs"))
	if !bs2.Exists(sha) || bs2.Exists(cacheSHA) {
		t.Fatal("blobs not restored as expected")
	}
}

func TestRejectsMaliciousArchive(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	tw.WriteHeader(&tar.Header{Name: "../../escape.txt", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "evil.tar.gz")
	os.WriteFile(p, buf.Bytes(), 0o600)
	if _, err := Verify(context.Background(), p, nil); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("path traversal not rejected: %v", err)
	}
}
