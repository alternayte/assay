package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// WebAuthHeader is the auth header that the fake developer portal under
// /web accepts.
const WebAuthHeader = "X-Portal-Token: " + SourceToken

// DefaultOffsite is the other host that the portal links to.
const DefaultOffsite = "https://status.acme.test"

// WebPage is one file of the fake developer portal.
type WebPage struct {
	Body    string
	Type    string
	Updated time.Time
}

// webEpoch is the update time of the corpus pages.
var webEpoch = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// loadWeb reads the portal files below corpus/web. A file serves at its
// path below /web; an .html file also serves without its extension, and
// index.html at its directory URL.
func (s *Sources) loadWeb() {
	_ = fs.WalkDir(corpus, "corpus/web", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := corpus.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "corpus/web/")
		s.web[rel] = &WebPage{Body: string(b), Type: webType(rel), Updated: webEpoch}
		return nil
	})
}

func webType(p string) string {
	switch path.Ext(p) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".xml":
		return "application/xml"
	}
	return "text/plain; charset=utf-8"
}

// PutWebPage adds or changes a portal file at its path below /web, such as
// guides/backoff.html.
func (s *Sources) PutWebPage(p string, page WebPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if page.Type == "" {
		page.Type = webType(p)
	}
	if page.Updated.IsZero() {
		page.Updated = s.now().UTC().Truncate(time.Second)
	}
	s.web[p] = &page
}

// DeleteWebPage removes a portal file.
func (s *Sources) DeleteWebPage(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.web, p)
}

// SetOffsite sets the other host that the portal pages link to.
func (s *Sources) SetOffsite(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsite = strings.TrimRight(u, "/")
}

// robotsTxt is the robots.txt of the fixture host. It refuses every
// crawler but Assay, and refuses Assay the internal pages of the portal.
const robotsTxt = `# The Acme developer portal.
User-agent: *
Disallow: /

User-agent: Assay
Disallow: /web/internal/
`

// serveWeb serves /robots.txt and the developer portal under /web, with an
// ETag and Last-Modified on each file. The portal needs the auth header.
func (s *Sources) serveWeb(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/robots.txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(robotsTxt))
		}
		return
	}
	name, value, _ := strings.Cut(WebAuthHeader, ": ")
	if r.Header.Get(name) != value {
		http.Error(w, "sign in to read the portal", http.StatusUnauthorized)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/web/")
	s.mu.Lock()
	page, ok := s.web[rel]
	switch {
	case ok:
	case rel == "" || strings.HasSuffix(rel, "/"):
		page, ok = s.web[rel+"index.html"]
	default:
		page, ok = s.web[rel+".html"]
	}
	offsite := s.offsite
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	body := strings.ReplaceAll(page.Body, "{{offsite}}", offsite)
	sum := sha256.Sum256([]byte(body))
	w.Header().Set("Content-Type", page.Type)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	w.Header().Set("Last-Modified", page.Updated.UTC().Format(http.TimeFormat))
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(body))
	}
}
