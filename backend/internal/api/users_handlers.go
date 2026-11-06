package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"lightmail/internal/auth"
	"lightmail/internal/providers"
	"lightmail/internal/store"
)

// Administration of the interface users (administrators only).

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cur := userFrom(r).ID
	out := make([]map[string]any, 0, len(list))
	for _, u := range list {
		out = append(out, userDTO(u, cur))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid user id")
		return
	}
	var req struct {
		IsAdmin *bool `json:"isAdmin"`
	}
	if !readJSON(w, r, &req, 4<<10) {
		return
	}
	if req.IsAdmin == nil {
		badRequest(w, "nothing to change")
		return
	}
	err := s.DB.SetUserAdmin(r.Context(), id, *req.IsAdmin)
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, http.StatusConflict, "last_admin", err.Error())
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	s.Log.Info("user role changed", "user_id", id, "admin", *req.IsAdmin, "by", userFrom(r).ID)
	w.WriteHeader(http.StatusNoContent)
}

// Usernames: letters, digits and . _ - @ +, so they stay easy to type and
// to pass to the command line.
var usernameRule = regexp.MustCompile(`^[\p{L}\p{N}._@+-]{1,64}$`)

func userDTO(u *store.User, cur int64) map[string]any {
	return map[string]any{"id": u.ID, "username": u.Username, "isAdmin": u.IsAdmin, "totpEnabled": u.TOTPEnabled, "createdAt": u.CreatedAt, "current": u.ID == cur}
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"isAdmin"`
	}
	if !readJSON(w, r, &req, 8<<10) {
		return
	}
	name := strings.TrimSpace(req.Username)
	if !usernameRule.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid_username", "the username must be 1 to 64 letters, digits or . _ - @ +")
		return
	}
	if len(req.Password) > 1024 {
		writeError(w, http.StatusBadRequest, "weak_password", "password too long")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if errors.Is(err, auth.ErrWeakPassword) {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.DB.CreateUser(r.Context(), name, hash, req.IsAdmin)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "username_taken", "this username is already used")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("user created", "user_id", u.ID, "admin", u.IsAdmin, "by", userFrom(r).ID)
	writeJSON(w, http.StatusCreated, userDTO(u, userFrom(r).ID))
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid user id")
		return
	}
	if id == userFrom(r).ID {
		writeError(w, http.StatusBadRequest, "self", "you cannot delete your own user")
		return
	}
	u, err := s.DB.UserByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if u.IsAdmin {
		// Same guard as the deletion itself, checked before touching the accounts.
		if users, err := s.DB.ListUsers(r.Context()); err == nil {
			admins := 0
			for _, x := range users {
				if x.IsAdmin {
					admins++
				}
			}
			if admins <= 1 {
				writeError(w, http.StatusConflict, "last_admin", store.ErrLastAdmin.Error())
				return
			}
		}
	}
	// Stop the synchronisation and revoke OAuth2 grants before the cascade.
	accounts, _ := s.DB.ListAccounts(r.Context(), id)
	for _, a := range accounts {
		s.Engine.AccountRemoved(a.ID)
		if a.AuthType == providers.AuthOAuth2 {
			if err := s.OAuth.Revoke(r.Context(), a); err != nil {
				s.Log.Warn("token revocation failed during user deletion", "account_id", a.ID, "err", err)
			}
		}
	}
	err = s.DB.DeleteUser(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, http.StatusConflict, "last_admin", err.Error())
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	s.Log.Info("user deleted", "user_id", id, "accounts", len(accounts), "by", userFrom(r).ID)
	w.WriteHeader(http.StatusNoContent)
}
