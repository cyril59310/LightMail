package mime

import (
	"bytes"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	xhtml "golang.org/x/net/html"
)

// The policy is built once; bluemonday policies are safe for concurrent use
// once configured.
var policy = buildPolicy()

func buildPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	// Presentational attributes used by real-world HTML emails.
	p.AllowAttrs("style", "class", "align", "valign", "width", "height", "bgcolor", "color", "border",
		"cellpadding", "cellspacing", "dir", "lang", "title").Globally()
	p.AllowAttrs("face", "size", "color").OnElements("font")
	p.AllowElements("font", "center", "span", "div", "u", "s", "strike", "small", "big", "hr", "wbr")
	// <style> blocks are kept: CSS cannot execute script in modern browsers
	// and remote loads (url(), @import) are blocked by the CSP of the body
	// endpoint unless the user allowed remote content.
	p.AllowUnsafe(true)
	p.AllowElements("style")
	p.AllowAttrs("media", "type").OnElements("style")
	p.AllowURLSchemes("http", "https", "mailto", "tel", "cid")
	p.AllowDataURIImages()
	p.RequireNoReferrerOnLinks(true)
	p.RequireNoFollowOnLinks(false)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.AllowRelativeURLs(false)
	p.SkipElementsContent("script", "title", "head", "noscript", "template", "object", "embed", "applet", "iframe", "frameset", "frame", "svg", "math", "xml")
	return p
}

// SanitizeOptions controls rendering of an HTML body.
type SanitizeOptions struct {
	AllowRemote bool
	// StripStyles drops <style> blocks (fragments inserted in the composer).
	StripStyles bool
	// CIDURL maps a Content-ID to a local URL serving the inline part.
	CIDURL func(cid string) (string, bool)
}

type SanitizeResult struct {
	HTML          string
	RemoteBlocked int
}

var cssURL = regexp.MustCompile(`(?i)url\(\s*['"]?\s*(https?:)?//`)
var cssImport = regexp.MustCompile(`(?i)@import`)

// Only raster images are allowed as data URIs (no SVG).
var rasterDataURI = regexp.MustCompile(`(?i)^data:image/(png|jpe?g|gif|webp|bmp);base64,[a-z0-9+/=\s]+$`)

// SanitizeHTML removes active content from untrusted HTML and rewrites image
// sources. Remote images are dropped unless AllowRemote is set.
func SanitizeHTML(src string, opts SanitizeOptions) SanitizeResult {
	clean := policy.Sanitize(src)
	var out bytes.Buffer
	out.Grow(len(clean))
	res := SanitizeResult{}
	z := xhtml.NewTokenizer(strings.NewReader(clean))
	inStyle := false
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		switch tt {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data == "style" {
				inStyle = tt == xhtml.StartTagToken
				if opts.StripStyles {
					continue
				}
			}
			changed := false
			attrs := tok.Attr[:0]
			for _, a := range tok.Attr {
				switch {
				case a.Key == "src" && tok.Data == "img":
					v, keep := rewriteSrc(a.Val, opts, &res)
					changed = true
					if !keep {
						attrs = append(attrs, xhtml.Attribute{Key: "data-lm-blocked", Val: "1"})
						continue
					}
					a.Val = v
				case a.Key == "style":
					if !opts.AllowRemote && cssURL.MatchString(a.Val) {
						res.RemoteBlocked++
					}
				}
				attrs = append(attrs, a)
			}
			tok.Attr = attrs
			if changed {
				out.WriteString(tok.String())
			} else {
				out.Write(z.Raw())
			}
		case xhtml.EndTagToken:
			if name, _ := z.TagName(); string(name) == "style" {
				inStyle = false
				if opts.StripStyles {
					continue
				}
			}
			out.Write(z.Raw())
		case xhtml.TextToken:
			raw := z.Raw()
			if inStyle && opts.StripStyles {
				continue
			}
			if inStyle && !opts.AllowRemote && (cssURL.Match(raw) || cssImport.Match(raw)) {
				res.RemoteBlocked++
			}
			out.Write(raw)
		default:
			out.Write(z.Raw())
		}
	}
	res.HTML = out.String()
	return res
}

func rewriteSrc(v string, opts SanitizeOptions, res *SanitizeResult) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "cid":
		cid := u.Opaque
		if cid == "" {
			cid = strings.TrimPrefix(v, "cid:")
		}
		cid, _ = url.PathUnescape(cid)
		if opts.CIDURL != nil {
			if local, ok := opts.CIDURL(strings.Trim(cid, "<>")); ok {
				return local, true
			}
		}
		return "", false
	case "data":
		if rasterDataURI.MatchString(v) {
			return v, true
		}
		return "", false
	case "http", "https":
		if opts.AllowRemote {
			return v, true
		}
		res.RemoteBlocked++
		return "", false
	}
	return "", false
}

// WrapDocument returns a complete HTML document for the sandboxed iframe.
func WrapDocument(body string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer">` +
		`<style>html,body{margin:0;padding:0;background:#fff;color:#111;}body{padding:16px;font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;word-wrap:break-word;overflow-wrap:anywhere;}img{max-width:100%;height:auto;}img[data-lm-blocked]{display:none;}pre{white-space:pre-wrap;}blockquote{margin:0 0 0 .8ex;border-left:2px solid #ccc;padding-left:1ex;}</style>` +
		`</head><body>` + body + `</body></html>`
}

// TextToHTML renders a plain-text body as safe HTML with clickable links.
func TextToHTML(s string) string {
	var b strings.Builder
	b.WriteString(`<pre style="font:inherit;white-space:pre-wrap;margin:0">`)
	last := 0
	for _, loc := range linkPattern.FindAllStringIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:loc[0]]))
		link := s[loc[0]:loc[1]]
		b.WriteString(`<a href="` + html.EscapeString(link) + `" target="_blank" rel="noopener noreferrer">` + html.EscapeString(link) + `</a>`)
		last = loc[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	b.WriteString(`</pre>`)
	return b.String()
}

var linkPattern = regexp.MustCompile(`https?://[^\s<>"'()]+[^\s<>"'().,;:!?]`)

// HTMLToText extracts readable text from HTML (for snippets and indexing).
func HTMLToText(s string) string {
	z := xhtml.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		tt := z.Next()
		switch tt {
		case xhtml.ErrorToken:
			return b.String()
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "style", "script", "head", "title":
				if tt == xhtml.StartTagToken {
					skip++
				}
			case "br", "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "table", "blockquote":
				b.WriteByte('\n')
			}
		case xhtml.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "style", "script", "head", "title":
				if skip > 0 {
					skip--
				}
			case "p", "div", "td":
				b.WriteByte(' ')
			}
		case xhtml.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		}
		if b.Len() > 1<<20 {
			return b.String()
		}
	}
}
