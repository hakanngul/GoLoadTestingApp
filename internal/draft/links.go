package draft

import (
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var (
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reScript  = regexp.MustCompile(`(?is)<(script|style|template)\b.*?</(script|style|template)\s*>`)
	reAnchor  = regexp.MustCompile(`(?is)<a\b[^>]*?\shref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
)

var staticExt = map[string]bool{
	".css": true, ".js": true, ".mjs": true, ".map": true, ".json": true, ".xml": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true, ".avif": true, ".ico": true, ".bmp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp4": true, ".webm": true, ".mp3": true, ".wav": true, ".ogg": true,
	".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".rar": true, ".7z": true,
	".exe": true, ".dmg": true, ".apk": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
}

var logoutRe = regexp.MustCompile(`(?i)(log[-_ ]?out|sign[-_ ]?out|log[-_ ]?off|sign[-_ ]?off|disconnect)`)

// Links returns the same-host links found in an HTML body as absolute URLs,
// in document order, without duplicates or fragments. mailto/tel/javascript
// links, static assets and logout-like links are skipped. At most max links
// are returned.
func Links(pageURL, body string, max int) []string {
	if max <= 0 {
		return nil
	}
	base, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || base.Host == "" {
		return nil
	}
	body = reComment.ReplaceAllString(body, "")
	body = reScript.ReplaceAllString(body, "")

	var out []string
	seen := map[string]bool{}
	for _, m := range reAnchor.FindAllStringSubmatch(body, -1) {
		href := m[1] + m[2] + m[3]
		href = strings.TrimSpace(html.UnescapeString(href))
		if href == "" || strings.HasPrefix(href, "#") {
			continue
		}
		u, err := base.Parse(href)
		if err != nil {
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		if !strings.EqualFold(u.Host, base.Host) {
			continue
		}
		u.Fragment, u.RawFragment = "", ""
		u.User = nil
		if u.Path == "" {
			u.Path = "/"
		}
		if staticExt[strings.ToLower(path.Ext(u.Path))] {
			continue
		}
		if logoutRe.MatchString(u.Path) || logoutRe.MatchString(u.RawQuery) {
			continue
		}
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		s := u.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}
