package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	lmime "lightmail/internal/mime"
	"lightmail/internal/store"
)

// Address book of the signed-in user. Every handler works on the user of
// the session only: contacts are never shared between users.

type contactInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Notes string `json:"notes"`
}

// validContact checks and normalises an address book entry.
func validContact(w http.ResponseWriter, in contactInput) (*store.SavedContact, bool) {
	a, err := lmime.ValidateAddress(store.Address{Name: in.Name, Addr: strings.TrimSpace(in.Email)})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_email", "invalid email address")
		return nil, false
	}
	notes := strings.TrimSpace(strings.ReplaceAll(in.Notes, "\x00", ""))
	if utf8.RuneCountInString(a.Name) > 200 || utf8.RuneCountInString(notes) > 2000 {
		badRequest(w, "name or notes too long")
		return nil, false
	}
	return &store.SavedContact{Name: a.Name, Email: a.Addr, Notes: notes}, true
}

func (s *Server) contactError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "contact_exists", "this address is already in the address book")
	case errors.Is(err, store.ErrContactLimit):
		writeError(w, http.StatusConflict, "contact_limit", "the address book is full")
	default:
		s.fail(w, r, err)
	}
}

func (s *Server) listSavedContacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		badRequest(w, "query too long")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.DB.SavedContacts(r.Context(), userFrom(r).ID, q, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) createSavedContact(w http.ResponseWriter, r *http.Request) {
	var in contactInput
	if !readJSON(w, r, &in, 16<<10) {
		return
	}
	c, ok := validContact(w, in)
	if !ok {
		return
	}
	c.UserID = userFrom(r).ID
	if err := s.DB.CreateSavedContact(r.Context(), c); err != nil {
		s.contactError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) updateSavedContact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid contact id")
		return
	}
	var in contactInput
	if !readJSON(w, r, &in, 16<<10) {
		return
	}
	c, ok := validContact(w, in)
	if !ok {
		return
	}
	c.ID, c.UserID = id, userFrom(r).ID
	if err := s.DB.UpdateSavedContact(r.Context(), c); err != nil {
		s.contactError(w, r, err)
		return
	}
	saved, err := s.DB.SavedContact(r.Context(), c.UserID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deleteSavedContact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid contact id")
		return
	}
	if err := s.DB.DeleteSavedContact(r.Context(), userFrom(r).ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
