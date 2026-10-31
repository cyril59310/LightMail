// Package mime handles MIME structure analysis, decoding, composition of
// outgoing messages and sanitisation of HTML bodies.
package mime

import (
	"encoding/json"
	"mime"
	"path"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"

	"lightmail/internal/store"
)

// TextPart locates a displayable body part inside a message.
type TextPart struct {
	Path     string `json:"p"`
	Type     string `json:"t"` // text/plain | text/html
	Encoding string `json:"e"`
	Charset  string `json:"c"`
	Size     int64  `json:"s"`
}

// Structure is what we keep from BODYSTRUCTURE (serialised in messages.structure).
type Structure struct {
	Plain *TextPart `json:"plain,omitempty"`
	HTML  *TextPart `json:"html,omitempty"`
}

func (s Structure) JSON() string {
	b, _ := json.Marshal(s)
	return string(b)
}

func ParseStructure(s string) Structure {
	var st Structure
	_ = json.Unmarshal([]byte(s), &st)
	return st
}

// Analysis is the result of walking a BODYSTRUCTURE.
type Analysis struct {
	Structure      Structure
	Attachments    []store.Attachment
	HasAttachments bool
}

func pathString(p []int) string {
	if len(p) == 0 {
		return "1"
	}
	parts := make([]string, len(p))
	for i, n := range p {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// PartPath converts "1.2" back to []int.
func PartPath(s string) ([]int, bool) {
	if s == "" {
		return nil, false
	}
	var out []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 10000 {
			return nil, false
		}
		out = append(out, n)
	}
	if len(out) > 20 {
		return nil, false
	}
	return out, true
}

// Analyze extracts body parts and attachments from a BODYSTRUCTURE.
func Analyze(bs imap.BodyStructure) Analysis {
	var a Analysis
	if bs == nil {
		return a
	}
	bs.Walk(func(p []int, part imap.BodyStructure) bool {
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true // descend into multiparts
		}
		mt := single.MediaType()
		disp := ""
		if d := single.Disposition(); d != nil {
			disp = strings.ToLower(d.Value)
		}
		filename := DecodeHeader(single.Filename())
		isBodyText := (mt == "text/plain" || mt == "text/html") && disp != "attachment" && (filename == "" || disp == "inline")
		if isBodyText {
			tp := &TextPart{Path: pathString(p), Type: mt, Encoding: strings.ToLower(single.Encoding), Charset: single.Params["charset"], Size: int64(single.Size)}
			if mt == "text/plain" && a.Structure.Plain == nil {
				a.Structure.Plain = tp
				return false
			}
			if mt == "text/html" && a.Structure.HTML == nil {
				a.Structure.HTML = tp
				return false
			}
		}
		cid := strings.Trim(single.ID, "<> ")
		inline := disp == "inline" || (disp == "" && cid != "" && strings.HasPrefix(mt, "image/"))
		if filename == "" {
			switch {
			case mt == "message/rfc822":
				filename = "message.eml"
			case cid != "":
				filename = cid
			case isBodyText:
				filename = "part-" + pathString(p) + extFor(mt)
			default:
				filename = "attachment-" + pathString(p) + extFor(mt)
			}
		}
		att := store.Attachment{
			Part:        pathString(p),
			Filename:    SanitizeFilename(filename),
			ContentType: mt,
			Encoding:    strings.ToLower(single.Encoding),
			Size:        int64(single.Size),
			ContentID:   cid,
			Inline:      inline && cid != "",
		}
		if att.Encoding == "base64" {
			att.Size = att.Size * 3 / 4 // approximate decoded size
		}
		a.Attachments = append(a.Attachments, att)
		if !att.Inline {
			a.HasAttachments = true
		}
		return false
	})
	return a
}

func extFor(mt string) string {
	if exts, _ := mime.ExtensionsByType(mt); len(exts) > 0 {
		return exts[0]
	}
	switch mt {
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	}
	return ".bin"
}

// SanitizeFilename keeps a safe display/download name: no path components,
// no control characters, bounded length. It is never used to build a
// filesystem path (cache files are named by SHA-256).
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		case r == 0x202e || r == 0x202d || r == 0x200e || r == 0x200f || (r >= 0x2066 && r <= 0x2069):
			// bidi overrides are used to disguise extensions (invoice_fdp.exe)
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ".")
	if out == "" || out == "_" {
		out = "attachment"
	}
	if r := []rune(out); len(r) > 180 {
		ext := path.Ext(out)
		if len([]rune(ext)) > 20 {
			ext = ""
		}
		out = string(r[:180-len([]rune(ext))]) + ext
	}
	return out
}

var wordDecoder = &mime.WordDecoder{CharsetReader: charsetReader}

// DecodeHeader decodes RFC 2047 encoded words, tolerating errors.
func DecodeHeader(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	if d, err := wordDecoder.DecodeHeader(s); err == nil {
		return d
	}
	return s
}
