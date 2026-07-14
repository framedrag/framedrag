package parse

import "bytes"

// htmlPrefixes are lowercase openings that mark a body as markup, not
// a feed. Matched against the first non-whitespace bytes only.
var htmlPrefixes = [][]byte{
	[]byte("<!doctype"),
	[]byte("<html"),
	[]byte("<head"),
	[]byte("<body"),
	[]byte("<!--"),
	[]byte("<?xml"),
	[]byte("<script"),
	[]byte("<title"),
	[]byte("<meta"),
	[]byte("<div"),
	[]byte("<span"),
	[]byte("<p>"),
	[]byte("<br"),
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// LooksLikeHTML reports whether body starts like an HTML/XML document.
// It is a fast pre-check for the health layer: origin servers and CDNs
// love serving error pages and challenge interstitials with HTTP 200.
// Parsers do not need it for safety (markup lines simply count as
// Rejected), but the pipeline can use it to short-circuit and label
// the failure precisely.
func LooksLikeHTML(body []byte) bool {
	b := bytes.TrimPrefix(body, utf8BOM)
	b = bytes.TrimLeft(b, " \t\r\n")
	if len(b) == 0 || b[0] != '<' {
		return false
	}
	head := b
	if len(head) > 16 {
		head = head[:16]
	}
	head = bytes.ToLower(head)
	for _, p := range htmlPrefixes {
		if bytes.HasPrefix(head, p) {
			return true
		}
	}
	return false
}
