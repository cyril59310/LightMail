package syncer

import (
	"context"
	"sort"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"lightmail/internal/events"
	"lightmail/internal/imapx"
	"lightmail/internal/mime"
	"lightmail/internal/store"
)

// folderSync synchronises folders of one account over one connection.
//
// Per folder, the algorithm is:
//  1. SELECT (with CONDSTORE when available); a UIDVALIDITY change drops the
//     local cache of the folder.
//  2. Fast path: nothing changed when UIDNEXT, EXISTS and HIGHESTMODSEQ
//     match the stored values.
//  3. New messages: UIDs above the last known UID are fetched in chunks
//     (envelope, flags, structure, a few headers) and upserted on
//     (folder, uid), so an interrupted sync resumes without duplicates.
//  4. Flags: CHANGEDSINCE with CONDSTORE, otherwise a periodic full refresh.
//  5. Expunges: when the message count does not add up, server UIDs are
//     compared with local ones (QRESYNC is not available in go-imap v2).
//  6. Backfill: older messages are fetched newest-first by sequence number,
//     a chunk at a time, within a time budget.
type folderSync struct {
	w        *worker
	a        *store.Account
	c        *imapx.Client
	ctx      context.Context
	full     bool
	inserted int
}

func (s *folderSync) chunk() int { return max(50, s.w.e.cfg.SyncChunkSize) }

func (s *folderSync) sync(f *store.Folder) error {
	db := s.w.e.db
	sel, err := s.c.SelectMailbox(f.Name, true)
	if err != nil {
		return err
	}
	condstore := s.c.HasCondStore() && sel.HighestModSeq > 0
	now := time.Now().Unix()

	if f.UIDValidity != 0 && sel.UIDValidity != f.UIDValidity {
		s.w.log.Info("UIDVALIDITY changed, resetting folder cache", "folder", f.Name)
		f.UIDValidity = sel.UIDValidity
		if err := db.ResetFolder(s.ctx, f); err != nil {
			return err
		}
		s.publish(f, 0)
	}
	f.UIDValidity = sel.UIDValidity

	flagDue := s.full || now-f.LastFlagSyncAt >= int64(s.w.e.cfg.SyncFlagRefreshPeriod.Seconds())
	if f.MaxUID != 0 && !s.full && sel.UIDNext != 0 && uint32(sel.UIDNext) == f.UIDNext && sel.NumMessages == f.NumMessages &&
		((condstore && sel.HighestModSeq == f.HighestModSeq) || (!condstore && !flagDue)) {
		f.LastSyncAt = now
		return db.SaveFolderState(s.ctx, f)
	}

	if sel.NumMessages == 0 {
		if f.MaxUID != 0 {
			if n, _ := db.CountFolderMessages(s.ctx, f.ID); n > 0 {
				uids, _ := db.FolderUIDs(s.ctx, f.ID)
				if _, err := db.DeleteMessagesByUID(s.ctx, f.ID, uids); err != nil {
					return err
				}
				s.publish(f, 0)
			}
		}
		f.NumMessages, f.UIDNext, f.HighestModSeq, f.BackfillDone = 0, uint32(sel.UIDNext), sel.HighestModSeq, true
		f.LastSyncAt, f.LastFlagSyncAt = now, now
		if f.MaxUID == 0 && sel.UIDNext > 1 {
			f.MaxUID = uint32(sel.UIDNext) - 1
			f.MinUID = 1
		}
		return db.SaveFolderState(s.ctx, f)
	}

	// A folder that was empty at its first sync has LastSyncAt set: new
	// messages are then incremental (and notified), not an initial import.
	initial := f.MaxUID == 0 && f.LastSyncAt == 0
	newCount := 0
	changed := 0
	if initial {
		// Most recent messages first, by sequence number.
		start := max(1, int(sel.NumMessages)-s.chunk()+1)
		ins, minUID, maxUID, err := s.fetchStore(f, seqRange(uint32(start), sel.NumMessages))
		if err != nil {
			return err
		}
		changed += ins
		f.MinUID, f.MaxUID = minUID, maxUID
		f.BackfillDone = start == 1
	} else {
		uids, err := s.c.UIDsInRange(imap.UID(f.MaxUID+1), 0)
		if err != nil {
			return err
		}
		var fresh []imap.UID
		for _, u := range uids {
			if uint32(u) > f.MaxUID {
				fresh = append(fresh, u)
			}
		}
		for i := 0; i < len(fresh); i += s.chunk() {
			part := fresh[i:min(len(fresh), i+s.chunk())]
			var set imap.UIDSet
			set.AddNum(part...)
			ins, _, maxUID, err := s.fetchStore(f, set)
			if err != nil {
				return err
			}
			newCount += len(part)
			changed += ins
			if f.MinUID == 0 {
				f.MinUID = minUIDOf(part)
			}
			if maxUID > f.MaxUID {
				f.MaxUID = maxUID
				if err := db.SaveFolderState(s.ctx, f); err != nil { // resumable cursor
					return err
				}
			}
			if ins > 0 && f.Role == store.RoleInbox {
				s.notifyNew(f, ins)
			}
		}

		// Flags
		if condstore && f.HighestModSeq > 0 && sel.HighestModSeq != f.HighestModSeq {
			n, err := s.syncFlags(f, f.HighestModSeq)
			if err != nil {
				return err
			}
			changed += n
		} else if !condstore && flagDue || condstore && f.HighestModSeq == 0 {
			n, err := s.syncFlags(f, 0)
			if err != nil {
				return err
			}
			changed += n
			f.LastFlagSyncAt = now
		}

		// Expunges: EXISTS(new) = EXISTS(old) + added - expunged.
		if int(sel.NumMessages) != int(f.NumMessages)+newCount || s.full {
			n, err := s.syncExpunges(f)
			if err != nil {
				return err
			}
			changed += n
		}
	}
	f.NumMessages = sel.NumMessages
	f.UIDNext = uint32(sel.UIDNext)
	if condstore {
		f.HighestModSeq = sel.HighestModSeq
	}
	if initial {
		f.LastFlagSyncAt = now
	}
	f.LastSyncAt = now
	if err := db.SaveFolderState(s.ctx, f); err != nil {
		return err
	}
	if changed > 0 {
		s.publish(f, newCount)
	}
	return nil
}

func seqRange(a, b uint32) imap.SeqSet {
	var s imap.SeqSet
	s.AddRange(a, b)
	return s
}

// fetchStore fetches headers for numSet, computes snippets and stores them.
func (s *folderSync) fetchStore(f *store.Folder, numSet imap.NumSet) (inserted int, minUID, maxUID uint32, err error) {
	var msgs []*store.Message
	err = s.c.FetchHeaders(numSet, func(b *imapclient.FetchMessageBuffer) error {
		m, _ := imapx.ToMessage(b)
		m.AccountID, m.FolderID = s.a.ID, f.ID
		msgs = append(msgs, m)
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}
	if len(msgs) == 0 {
		return 0, 0, 0, nil
	}
	s.fillSnippets(msgs)
	ids, err := s.w.e.db.UpsertMessages(s.ctx, msgs)
	if err != nil {
		return 0, 0, 0, err
	}
	s.w.e.m.MessagesSync.Add(int64(len(ids)))
	s.inserted += len(ids)
	minUID = msgs[0].UID
	for _, m := range msgs {
		minUID = min(minUID, m.UID)
		maxUID = max(maxUID, m.UID)
	}
	return len(ids), minUID, maxUID, nil
}

// fillSnippets fetches the first bytes of the main text part of each
// message, grouped by part path to minimise round trips.
func (s *folderSync) fillSnippets(msgs []*store.Message) {
	type key struct {
		path string
		html bool
	}
	groups := map[key][]*store.Message{}
	parts := map[*store.Message]*mime.TextPart{}
	for _, m := range msgs {
		st := mime.ParseStructure(m.Structure)
		tp := st.Plain
		if tp == nil {
			tp = st.HTML
		}
		if tp == nil {
			continue
		}
		parts[m] = tp
		k := key{tp.Path, tp.Type == "text/html"}
		groups[k] = append(groups[k], m)
	}
	for k, list := range groups {
		path, ok := mime.PartPath(k.path)
		if !ok {
			continue
		}
		size := int64(1024)
		if k.html {
			size = 6144 // HTML often starts with a large <style> block
		}
		var set imap.UIDSet
		byUID := map[imap.UID]*store.Message{}
		for _, m := range list {
			set.AddNum(imap.UID(m.UID))
			byUID[imap.UID(m.UID)] = m
		}
		sec := &imap.FetchItemBodySection{Part: path, Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: size}}
		cmd := s.c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}})
		for {
			msg := cmd.Next()
			if msg == nil {
				break
			}
			buf, err := msg.Collect()
			if err != nil || buf.UID == 0 {
				continue
			}
			m := byUID[buf.UID]
			if m == nil || len(buf.BodySection) == 0 {
				continue
			}
			tp := parts[m]
			text := mime.DecodePartial(buf.BodySection[0].Bytes, tp.Encoding, tp.Charset)
			m.Snippet = mime.Snippet(text, k.html, 200)
		}
		if err := cmd.Close(); err != nil {
			s.w.log.Debug("snippet fetch failed", "err", err)
		}
	}
}

// syncFlags refreshes flags for the local UID window.
func (s *folderSync) syncFlags(f *store.Folder, changedSince uint64) (int, error) {
	var states []imapx.FlagState
	if changedSince > 0 {
		var set imap.UIDSet
		set.AddRange(imap.UID(max(1, f.MinUID)), 0)
		st, err := s.c.FetchFlags(set, changedSince)
		if err != nil {
			return 0, err
		}
		states = st
	} else {
		local, err := s.w.e.db.FolderUIDs(s.ctx, f.ID)
		if err != nil {
			return 0, err
		}
		for i := 0; i < len(local); i += 2000 {
			part := local[i:min(len(local), i+2000)]
			var set imap.UIDSet
			set.AddRange(imap.UID(part[0]), imap.UID(part[len(part)-1]))
			st, err := s.c.FetchFlags(set, 0)
			if err != nil {
				return 0, err
			}
			states = append(states, st...)
		}
	}
	if len(states) == 0 {
		return 0, nil
	}
	ups := make([]store.FlagUpdate, 0, len(states))
	for _, st := range states {
		u := store.FlagUpdate{UID: uint32(st.UID), ModSeq: st.ModSeq}
		u.Seen, u.Flagged, u.Answered, u.Draft, u.Forwarded, u.Keywords = imapx.FlagsToState(st.Flags)
		ups = append(ups, u)
	}
	return s.w.e.db.ApplyFlagUpdates(s.ctx, f.ID, ups)
}

// syncExpunges deletes local messages that disappeared from the server.
func (s *folderSync) syncExpunges(f *store.Folder) (int, error) {
	if f.MinUID == 0 {
		return 0, nil
	}
	server, err := s.c.UIDsInRange(imap.UID(f.MinUID), 0)
	if err != nil {
		return 0, err
	}
	local, err := s.w.e.db.FolderUIDs(s.ctx, f.ID)
	if err != nil {
		return 0, err
	}
	present := make(map[uint32]struct{}, len(server))
	for _, u := range server {
		present[uint32(u)] = struct{}{}
	}
	var gone []uint32
	for _, u := range local {
		if _, ok := present[u]; !ok {
			gone = append(gone, u)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	return s.w.e.db.DeleteMessagesByUID(s.ctx, f.ID, gone)
}

// backfill fetches one chunk of messages older than the local window.
func (s *folderSync) backfill(f *store.Folder) error {
	db := s.w.e.db
	if max := s.w.e.cfg.SyncMaxPerFolder; max > 0 {
		if n, _ := db.CountFolderMessages(s.ctx, f.ID); n >= max {
			f.BackfillDone = true
			return db.SaveFolderState(s.ctx, f)
		}
	}
	if f.MinUID <= 1 {
		f.BackfillDone = true
		return db.SaveFolderState(s.ctx, f)
	}
	if s.c.Selected() != f.Name {
		if _, err := s.c.SelectMailbox(f.Name, false); err != nil {
			return err
		}
	}
	seq, err := s.c.SeqOfUID(imap.UID(f.MinUID))
	if err != nil {
		return err
	}
	var numSet imap.NumSet
	if seq > 0 {
		if seq == 1 {
			f.BackfillDone = true
			return db.SaveFolderState(s.ctx, f)
		}
		numSet = seqRange(uint32(max(1, int(seq)-s.chunk())), seq-1)
	} else {
		// The oldest known message was expunged: search older UIDs.
		uids, err := s.c.UIDsInRange(1, imap.UID(f.MinUID-1))
		if err != nil {
			return err
		}
		if len(uids) == 0 {
			f.BackfillDone = true
			return db.SaveFolderState(s.ctx, f)
		}
		sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
		uids = uids[max(0, len(uids)-s.chunk()):]
		var set imap.UIDSet
		set.AddNum(uids...)
		numSet = set
	}
	ins, minUID, _, err := s.fetchStore(f, numSet)
	if err != nil {
		return err
	}
	if minUID == 0 || minUID >= f.MinUID {
		f.BackfillDone = true
	} else {
		f.MinUID = minUID
	}
	if err := db.SaveFolderState(s.ctx, f); err != nil {
		return err
	}
	if ins > 0 {
		s.publish(f, 0)
	}
	return nil
}

func (s *folderSync) publish(f *store.Folder, newCount int) {
	s.w.e.hub.Publish(s.a.UserID, events.Event{Type: events.TypeMessages, Data: map[string]any{"accountId": s.a.ID, "folderId": f.ID, "role": f.Role, "new": newCount}})
}

func (s *folderSync) notifyNew(f *store.Folder, n int) {
	msgs, err := s.w.e.db.ListMessages(s.ctx, store.MessageFilter{UserID: s.a.UserID, FolderID: f.ID, Limit: 1})
	if err != nil || len(msgs) == 0 || msgs[0].Seen {
		return
	}
	m := msgs[0]
	from := m.From.Name
	if from == "" {
		from = m.From.Addr
	}
	s.w.e.hub.Publish(s.a.UserID, events.Event{Type: events.TypeNewMail, Data: map[string]any{
		"accountId": s.a.ID, "folderId": f.ID, "count": n, "messageId": m.ID, "from": from, "subject": m.Subject, "email": s.a.Email,
	}})
	s.w.e.notifier.Notify(s.ctx, s.a.UserID, s.a.ID, events.KindNewMail, from, m.Subject)
}

func minUIDOf(uids []imap.UID) uint32 {
	m := uids[0]
	for _, u := range uids {
		m = min(m, u)
	}
	return uint32(m)
}
