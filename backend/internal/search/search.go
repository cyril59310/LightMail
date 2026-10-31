// Package search parses Gmail-style search queries into local filters and
// IMAP SEARCH criteria.
//
// Supported operators: from: to: cc: subject: has:attachment is:unread
// is:read is:starred is:unstarred after:YYYY-MM-DD before:YYYY-MM-DD
// newer_than:Nd|Nw|Nm|Ny older_than:... in:<role> account:<email>
// "quoted phrases" and free terms (matched against indexed text).
//
// Free text is matched against subject, sender, recipients, snippet and the
// bodies of messages already downloaded; server-side search (IMAP SEARCH
// TEXT) covers full bodies of a folder.
package search

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"

	"lightmail/internal/store"
)

type Query struct {
	From, To, Subject []string
	Terms             []string // free text (FTS)
	Excluded          []string // -term
	HasAttachment     *bool
	Unread            *bool
	Starred           *bool
	After, Before     time.Time
	In                string // folder role
	Account           string
	Unknown           []string // unsupported operators, reported to the user
}

var roles = map[string]string{
	"inbox": store.RoleInbox, "sent": store.RoleSent, "drafts": store.RoleDrafts, "draft": store.RoleDrafts,
	"trash": store.RoleTrash, "bin": store.RoleTrash, "spam": store.RoleJunk, "junk": store.RoleJunk, "archive": store.RoleArchive,
	"envoyes": store.RoleSent, "brouillons": store.RoleDrafts, "corbeille": store.RoleTrash,
}

func ptr(b bool) *bool { return &b }

// Parse parses q using loc for dates.
func Parse(q string, loc *time.Location) Query {
	if loc == nil {
		loc = time.UTC
	}
	var out Query
	for _, tok := range tokenize(q) {
		neg := strings.HasPrefix(tok, "-") && len(tok) > 1
		if neg {
			tok = tok[1:]
		}
		key, val, hasOp := strings.Cut(tok, ":")
		if !hasOp || val == "" || strings.ContainsAny(key, " \"") {
			if term := strings.Trim(tok, `"`); term != "" {
				if neg {
					out.Excluded = append(out.Excluded, term)
				} else {
					out.Terms = append(out.Terms, term)
				}
			}
			continue
		}
		val = strings.Trim(val, `"`)
		switch strings.ToLower(key) {
		case "from":
			out.From = append(out.From, val)
		case "to", "cc":
			out.To = append(out.To, val)
		case "subject":
			out.Subject = append(out.Subject, val)
		case "has":
			if strings.HasPrefix(strings.ToLower(val), "attach") {
				out.HasAttachment = ptr(!neg)
			} else {
				out.Unknown = append(out.Unknown, tok)
			}
		case "is":
			switch strings.ToLower(val) {
			case "unread":
				out.Unread = ptr(!neg)
			case "read":
				out.Unread = ptr(neg)
			case "starred", "flagged":
				out.Starred = ptr(!neg)
			case "unstarred":
				out.Starred = ptr(neg)
			default:
				out.Unknown = append(out.Unknown, tok)
			}
		case "after", "since":
			if t, ok := parseDate(val, loc); ok {
				out.After = t
			} else {
				out.Unknown = append(out.Unknown, tok)
			}
		case "before":
			if t, ok := parseDate(val, loc); ok {
				out.Before = t
			} else {
				out.Unknown = append(out.Unknown, tok)
			}
		case "newer_than", "older_than":
			d, ok := parseRelative(val)
			if !ok {
				out.Unknown = append(out.Unknown, tok)
				continue
			}
			t := time.Now().In(loc).Add(-d)
			if strings.ToLower(key) == "newer_than" {
				out.After = t
			} else {
				out.Before = t
			}
		case "in", "label":
			if r, ok := roles[strings.ToLower(val)]; ok {
				out.In = r
			} else {
				out.Unknown = append(out.Unknown, tok)
			}
		case "account":
			out.Account = strings.ToLower(val)
		default:
			// "word:with:colons" or URLs: treat as plain text
			out.Terms = append(out.Terms, strings.Trim(tok, `"`))
		}
	}
	return out
}

func tokenize(q string) []string {
	var toks []string
	var cur strings.Builder
	inQuote := false
	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case unicode.IsSpace(r) && !inQuote:
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
		if len(toks) > 50 {
			break
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

func parseDate(s string, loc *time.Location) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "2006/01/02", "02/01/2006", "2006-01", "2006"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseRelative(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 0 || n > 10000 {
		return 0, false
	}
	day := 24 * time.Hour
	switch s[len(s)-1] {
	case 'd':
		return time.Duration(n) * day, true
	case 'w':
		return time.Duration(n) * 7 * day, true
	case 'm':
		return time.Duration(n) * 30 * day, true
	case 'y':
		return time.Duration(n) * 365 * day, true
	}
	return 0, false
}

// ftsQuote returns an FTS5 string literal with prefix matching. User input
// is always quoted, so FTS operators in queries are inert.
func ftsQuote(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"*`
}

// FTS builds the FTS5 MATCH expression for free terms ("" if none).
func (q Query) FTS() string {
	var parts []string
	for _, t := range q.Terms {
		parts = append(parts, ftsQuote(t))
	}
	if len(parts) == 0 {
		return ""
	}
	expr := strings.Join(parts, " AND ")
	for _, t := range q.Excluded {
		expr += " NOT " + ftsQuote(t)
	}
	return expr
}

// Filter applies the query to a store filter.
func (q Query) Filter(f *store.MessageFilter) {
	f.From = append(f.From, q.From...)
	f.To = append(f.To, q.To...)
	f.Subject = append(f.Subject, q.Subject...)
	f.HasAtt = q.HasAttachment
	f.Unread = q.Unread
	f.Flagged = q.Starred
	if !q.After.IsZero() {
		f.After = q.After.Unix()
	}
	if !q.Before.IsZero() {
		f.Before = q.Before.Unix()
	}
	if q.In != "" && f.FolderID == 0 {
		f.Role = q.In
	} else if f.FolderID == 0 && q.In == "" {
		f.ExcludeTrash = true
	}
	f.FTS = q.FTS()
}

// IMAP converts the query into IMAP SEARCH criteria (server-side search).
// has:attachment cannot be expressed in standard IMAP and is applied
// locally afterwards.
func (q Query) IMAP() *imap.SearchCriteria {
	c := &imap.SearchCriteria{}
	for _, v := range q.From {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: v})
	}
	for _, v := range q.To {
		c.Or = append(c.Or, [2]imap.SearchCriteria{
			{Header: []imap.SearchCriteriaHeaderField{{Key: "To", Value: v}}},
			{Header: []imap.SearchCriteriaHeaderField{{Key: "Cc", Value: v}}},
		})
	}
	for _, v := range q.Subject {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "Subject", Value: v})
	}
	c.Text = append(c.Text, q.Terms...)
	for _, v := range q.Excluded {
		c.Not = append(c.Not, imap.SearchCriteria{Text: []string{v}})
	}
	if q.Unread != nil {
		if *q.Unread {
			c.NotFlag = append(c.NotFlag, imap.FlagSeen)
		} else {
			c.Flag = append(c.Flag, imap.FlagSeen)
		}
	}
	if q.Starred != nil {
		if *q.Starred {
			c.Flag = append(c.Flag, imap.FlagFlagged)
		} else {
			c.NotFlag = append(c.NotFlag, imap.FlagFlagged)
		}
	}
	if !q.After.IsZero() {
		c.Since = q.After
	}
	if !q.Before.IsZero() {
		c.Before = q.Before
	}
	return c
}

// Describe summarises unsupported parts for the UI.
func (q Query) Describe() string {
	if len(q.Unknown) == 0 {
		return ""
	}
	return fmt.Sprintf("unsupported operators ignored: %s", strings.Join(q.Unknown, " "))
}
