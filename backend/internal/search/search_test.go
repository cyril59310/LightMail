package search

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"lightmail/internal/store"
)

func TestParseOperators(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	q := Parse(`from:contact@example.com to:moi@x.fr subject:"facture mars" has:attachment is:unread after:2026-01-01 before:2026-12-31 in:sent devis -spam bizarre:op`, loc)
	if len(q.From) != 1 || q.From[0] != "contact@example.com" || q.To[0] != "moi@x.fr" || q.Subject[0] != "facture mars" {
		t.Fatalf("fields: %+v", q)
	}
	if q.HasAttachment == nil || !*q.HasAttachment || q.Unread == nil || !*q.Unread || q.In != store.RoleSent {
		t.Fatalf("flags: %+v", q)
	}
	if q.After.Year() != 2026 || q.After.Location() != loc || q.Before.Month() != 12 {
		t.Fatalf("dates: %v %v", q.After, q.Before)
	}
	if len(q.Terms) != 2 || q.Terms[0] != "devis" || len(q.Excluded) != 1 || q.Excluded[0] != "spam" {
		t.Fatalf("terms: %+v %+v", q.Terms, q.Excluded)
	}
	if q.FTS() != `"devis"* AND "bizarre:op"* NOT "spam"*` {
		t.Fatalf("fts: %s", q.FTS())
	}
}

func TestFTSInjectionIsInert(t *testing.T) {
	q := Parse(`a" OR subject:* NEAR(x`, nil)
	expr := q.FTS()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, _ := db.CreateUser(context.Background(), "u", "h", true)
	// Must not produce an FTS syntax error.
	if _, err := db.ListMessages(context.Background(), store.MessageFilter{UserID: u.ID, FTS: expr}); err != nil {
		t.Fatalf("query %q failed: %v", expr, err)
	}
}

func TestRelativeAndIMAP(t *testing.T) {
	q := Parse("newer_than:7d is:starred from:bob", nil)
	if time.Since(q.After) < 6*24*time.Hour || time.Since(q.After) > 8*24*time.Hour {
		t.Fatal("newer_than")
	}
	c := q.IMAP()
	if len(c.Header) != 1 || len(c.Flag) != 1 || c.Since.IsZero() {
		t.Fatalf("imap criteria %+v", c)
	}
	if Parse("is:whatever", nil).Describe() == "" {
		t.Fatal("unknown operator not reported")
	}
}
