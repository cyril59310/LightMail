package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"lightmail/internal/store"
)

func writeDBMetrics(w io.Writer, db *store.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := db.Stats(ctx)
	if err != nil {
		return
	}
	for _, g := range []struct {
		name, help string
		v          int64
	}{
		{"lightmail_accounts", "Configured mail accounts.", int64(s.Accounts)},
		{"lightmail_folders", "Known folders.", int64(s.Folders)},
		{"lightmail_messages_cached", "Message headers in the local cache.", int64(s.Messages)},
		{"lightmail_bodies_cached", "Message bodies in the local cache.", int64(s.Bodies)},
		{"lightmail_blob_bytes", "Bytes used by cached files.", s.BlobBytes},
		{"lightmail_database_bytes", "SQLite database size.", s.DBBytes},
	} {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", g.name, g.help, g.name, g.name, g.v)
	}
}
