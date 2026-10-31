package blobs

import (
	"io"
	"strings"
	"testing"
)

func TestPutDedupLimitAndTraversal(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, n, err := s.Put(strings.NewReader("hello"), 10)
	if err != nil || n != 5 {
		t.Fatal(err)
	}
	b, _, _ := s.Put(strings.NewReader("hello"), 10)
	if a != b {
		t.Fatal("dedup failed")
	}
	count := 0
	s.Walk(func(string, int64) error { count++; return nil })
	if count != 1 {
		t.Fatalf("expected 1 stored file, got %d", count)
	}
	if _, _, err := s.Put(strings.NewReader("this is too long"), 4); err != ErrTooLarge {
		t.Fatalf("limit not enforced: %v", err)
	}
	f, err := s.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if string(got) != "hello" {
		t.Fatal("content mismatch")
	}
	for _, bad := range []string{"../../etc/passwd", "..", "ab/../../x", strings.Repeat("g", 64)} {
		if _, err := s.Open(bad); err != ErrInvalid {
			t.Errorf("%q: expected ErrInvalid, got %v", bad, err)
		}
	}
}
