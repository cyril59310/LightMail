package mime

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message/charset"
)

func charsetReader(cs string, input io.Reader) (io.Reader, error) {
	return charset.Reader(cs, input)
}

// DecodeTransfer wraps r with a Content-Transfer-Encoding decoder.
func DecodeTransfer(r io.Reader, encoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &whitespaceStripper{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	default:
		return r
	}
}

// DecodeText decodes a complete text part to UTF-8.
func DecodeText(raw []byte, encoding, cs string) string {
	data, _ := io.ReadAll(DecodeTransfer(bytes.NewReader(raw), encoding))
	return ToUTF8(data, cs)
}

// ToUTF8 converts bytes in charset cs to valid UTF-8.
func ToUTF8(data []byte, cs string) string {
	cs = strings.ToLower(strings.TrimSpace(cs))
	if cs != "" && cs != "utf-8" && cs != "us-ascii" && cs != "utf8" {
		if r, err := charset.Reader(cs, bytes.NewReader(data)); err == nil {
			if out, err := io.ReadAll(r); err == nil {
				data = out
			}
		}
	}
	if !utf8.Valid(data) {
		return strings.ToValidUTF8(string(data), "�")
	}
	return string(data)
}

// DecodePartial decodes the beginning of a part fetched with a byte range
// (used for snippets): incomplete trailing sequences are dropped.
func DecodePartial(raw []byte, encoding, cs string) string {
	switch strings.ToLower(encoding) {
	case "base64":
		clean := make([]byte, 0, len(raw))
		for _, c := range raw {
			if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
				clean = append(clean, c)
			}
		}
		clean = clean[:len(clean)-len(clean)%4]
		dec := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, _ := base64.StdEncoding.Decode(dec, clean)
		raw = dec[:n]
	case "quoted-printable":
		if i := bytes.LastIndexByte(raw, '='); i >= 0 && i > len(raw)-3 {
			raw = raw[:i]
		}
		out, _ := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		raw = out
	}
	// drop a truncated UTF-8 sequence at the end
	if cs == "" || strings.EqualFold(cs, "utf-8") {
		for i := 0; i < 3 && len(raw) > 0 && !utf8.Valid(raw); i++ {
			if r, size := utf8.DecodeLastRune(raw); r != utf8.RuneError || size != 1 {
				break
			}
			raw = raw[:len(raw)-1]
		}
	}
	return ToUTF8(raw, cs)
}

type whitespaceStripper struct{ r io.Reader }

func (w *whitespaceStripper) Read(p []byte) (int, error) {
	for {
		n, err := w.r.Read(p)
		j := 0
		for i := 0; i < n; i++ {
			if c := p[i]; c != '\r' && c != '\n' && c != ' ' && c != '\t' {
				p[j] = c
				j++
			}
		}
		if j > 0 || err != nil {
			return j, err
		}
	}
}

// Snippet builds a short preview from text or HTML content.
func Snippet(s string, isHTML bool, maxRunes int) string {
	if isHTML {
		s = HTMLToText(s)
	}
	var b strings.Builder
	space := false
	count := 0
	for _, r := range s {
		if r == 0x00A0 || r == 0x200C || r == 0x200B || r == 0x034F || r == 0xFEFF {
			r = ' '
		}
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
				count++
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
		count++
		if count >= maxRunes {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
