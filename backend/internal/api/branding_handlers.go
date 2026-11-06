package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"lightmail/internal/store"
)

// The instance logo replaces the default brand mark (sidebar, login page,
// favicon). It is stored in the meta table so that backups include it.
const (
	logoMetaKey  = "branding.logo"
	maxLogoBytes = 512 << 10
)

// Raster formats are recognised by their signature; SVG by its root element.
var logoTypes = []struct {
	mime  string
	match func([]byte) bool
}{
	{"image/png", func(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) }},
	{"image/jpeg", func(b []byte) bool { return bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}) }},
	{"image/gif", func(b []byte) bool {
		return bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))
	}},
	{"image/webp", func(b []byte) bool { return len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" }},
	{"image/x-icon", func(b []byte) bool { return bytes.HasPrefix(b, []byte{0, 0, 1, 0}) }},
	{"image/svg+xml", func(b []byte) bool {
		head := bytes.ToLower(b[:min(len(b), 4096)])
		return bytes.Contains(head, []byte("<svg"))
	}},
}

type storedLogo struct {
	Type    string `json:"type"`
	Data    string `json:"data"` // base64
	Version int64  `json:"v"`
}

func (s *Server) loadLogo(ctx context.Context) (*storedLogo, error) {
	v, err := s.DB.GetMeta(ctx, logoMetaKey)
	if err != nil {
		return nil, err
	}
	var l storedLogo
	if err := json.Unmarshal([]byte(v), &l); err != nil || l.Type == "" {
		return nil, store.ErrNotFound
	}
	return &l, nil
}

// logoVersion is 0 when the default logo is in use.
func (s *Server) logoVersion(ctx context.Context) int64 {
	if l, err := s.loadLogo(ctx); err == nil {
		return l.Version
	}
	return 0
}

// getLogo is public: the login page shows it.
func (s *Server) getLogo(w http.ResponseWriter, r *http.Request) {
	l, err := s.loadLogo(r.Context())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := base64.StdEncoding.DecodeString(l.Data)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", l.Type)
	h.Set("X-Content-Type-Options", "nosniff")
	// An SVG opened directly must not run scripts or load anything.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("ETag", `"`+strconv.FormatInt(l.Version, 36)+`"`)
	http.ServeContent(w, r, "", time.Unix(0, l.Version), bytes.NewReader(data))
}

func (s *Server) putLogo(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r).IsAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "only an administrator can change the logo")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLogoBytes+64<<10)
	file, _, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the logo exceeds 512 KB")
			return
		}
		badRequest(w, "multipart/form-data body with a \"file\" field expected")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxLogoBytes+1))
	if err != nil {
		badRequest(w, "unreadable file")
		return
	}
	if len(data) > maxLogoBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the logo exceeds 512 KB")
		return
	}
	typ := ""
	for _, t := range logoTypes {
		if t.match(data) {
			typ = t.mime
			break
		}
	}
	if typ == "" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_type", "use a PNG, JPEG, GIF, WebP, ICO or SVG image")
		return
	}
	l := storedLogo{Type: typ, Data: base64.StdEncoding.EncodeToString(data), Version: time.Now().UnixNano()}
	v, _ := json.Marshal(l)
	if err := s.DB.SetMeta(r.Context(), logoMetaKey, string(v)); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logoVersion": l.Version})
}

func (s *Server) deleteLogo(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r).IsAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "only an administrator can change the logo")
		return
	}
	if err := s.DB.SetMeta(r.Context(), logoMetaKey, "{}"); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
