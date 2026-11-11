package api

import "testing"

func TestPreviewType(t *testing.T) {
	cases := []struct{ ct, name, want string }{
		{"image/png", "a.png", "image/png"},
		{"IMAGE/JPG", "photo", "image/jpeg"},
		{"application/pdf; name=x.pdf", "x.pdf", "application/pdf"},
		{"application/octet-stream", "Facture.PDF", "application/pdf"},
		{"application/octet-stream", "song.mp3", "audio/mpeg"},
		{"", "clip.webm", "video/webm"},
		{"application/octet-stream", "page.html", ""},
		{"text/html", "page.html", ""},
		{"image/svg+xml", "logo.svg", ""},
		{"application/zip", "photo.png", ""}, // a declared type is never overridden by the name
	}
	for _, c := range cases {
		if got := previewType(c.ct, c.name); got != c.want {
			t.Errorf("previewType(%q, %q) = %q, want %q", c.ct, c.name, got, c.want)
		}
	}
}
