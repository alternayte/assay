// Package web reads a documentation web site, such as a developer portal or
// a vendor docs site. It starts from a seed: llms.txt, llms-full.txt,
// sitemap.xml or a start page. It follows links on the same scheme and host
// only, inside the path prefixes, breadth first, up to a depth and a page
// limit. Each page is an item.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"slices"
	"strings"
	gosync "sync"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/outbound"
)

// The seeds of a connection.
const (
	SeedAuto     = "auto"
	SeedLLMs     = "llms"
	SeedLLMsFull = "llms-full"
	SeedSitemap  = "sitemap"
	SeedStart    = "start"
)

// The defaults and bounds of the limits. The limits bound a site with
// generated pages.
const (
	DefaultMaxDepth = 3
	DefaultMaxPages = 2000
	maxDepthLimit   = 20
	maxPagesLimit   = 100000
	// maxSitemaps bounds the sitemap files of a sitemap index.
	maxSitemaps = 100
)

// SitePart is the key of the one part of a web connection: a crawl needs
// the pages that it already visited.
const SitePart = "site"

// The skip reasons of a page.
const (
	ReasonForeignRedirect = "The page redirects to another host."
	ReasonNotPage         = "The page is not HTML or Markdown."
	ReasonRobots          = "robots.txt does not allow Assay to read the page."
	ReasonPageLimit       = "The site has more pages than the page limit."
)

// ErrNoPage means that the crawl read no page from the seed. The run
// fails, and the stored pages stay.
var ErrNoPage = errors.New("web: the crawl read no page from the seed. Check the base URL, the seed and the path prefixes")

// Scope selects the pages of a connection.
type Scope struct {
	Seed         string   `json:"seed,omitempty" enum:"auto,llms,llms-full,sitemap,start" doc:"Where the crawl starts. auto tries /llms.txt, then /sitemap.xml, then the base URL."`
	PathPrefixes []string `json:"pathPrefixes,omitempty" doc:"Path prefixes of the pages to read. Empty means the path of the base URL."`
	MaxDepth     *int     `json:"maxDepth,omitempty" minimum:"0" maximum:"20" doc:"The most links from a seed page. Empty means 3."`
	MaxPages     int      `json:"maxPages,omitempty" minimum:"0" maximum:"100000" doc:"The most pages to read. Empty means 2000."`
}

// ParseScope reads and checks the scope of a connection, with the
// defaults set.
func ParseScope(raw json.RawMessage) (Scope, error) {
	var s Scope
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, fmt.Errorf("web: the scope is not valid: %w", err)
		}
	}
	if s.Seed == "" {
		s.Seed = SeedAuto
	}
	if !slices.Contains([]string{SeedAuto, SeedLLMs, SeedLLMsFull, SeedSitemap, SeedStart}, s.Seed) {
		return s, fmt.Errorf("web: the seed %q is not auto, llms, llms-full, sitemap or start", s.Seed)
	}
	if s.MaxDepth == nil {
		d := DefaultMaxDepth
		s.MaxDepth = &d
	}
	if *s.MaxDepth < 0 || *s.MaxDepth > maxDepthLimit {
		return s, fmt.Errorf("web: the depth must be 0 to %d", maxDepthLimit)
	}
	if s.MaxPages == 0 {
		s.MaxPages = DefaultMaxPages
	}
	if s.MaxPages < 1 || s.MaxPages > maxPagesLimit {
		return s, fmt.Errorf("web: the page limit must be 1 to %d", maxPagesLimit)
	}
	for i, p := range s.PathPrefixes {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, "/") {
			return s, fmt.Errorf("web: the path prefix %q does not start with /", p)
		}
		s.PathPrefixes[i] = p
	}
	return s, nil
}

// ParseAuth splits the auth header of a connection, "Name: value". An
// empty secret sends no header.
func ParseAuth(secret string) (name, value string, err error) {
	if secret == "" {
		return "", "", nil
	}
	name, value, ok := strings.Cut(secret, ":")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || name == "" || value == "" || strings.ContainsAny(name, " \t\r\n") || strings.ContainsAny(value, "\r\n") {
		return "", "", errors.New("web: the auth header must be a header name and a value, such as X-Portal-Token: abc")
	}
	return textproto.CanonicalMIMEHeaderKey(name), value, nil
}

// Connector reads one web site.
type Connector struct {
	c *connectors.Client
	// auth is set when the connection sends an auth header. A 403 then
	// refuses the header, and sync pauses the connection. Without one, a
	// 403 skips the page, as a static host answers for a missing file.
	auth     bool
	base     *url.URL
	key      string
	scope    Scope
	prefixes []string
	limits   connectors.Limits
	known    []connectors.Known

	robotsOnce gosync.Once
	robots     *robots
	robotsErr  error

	fullOnce gosync.Once
	full     []*content.Item
	fullRev  string
	fullErr  error

	mu      gosync.Mutex
	skipped []connectors.Skipped
}

// New returns a connector. scope is the JSON scope of the connection.
func New(c *connectors.Client, scope json.RawMessage, auth bool, limits connectors.Limits) (*Connector, error) {
	s, err := ParseScope(scope)
	if err != nil {
		return nil, err
	}
	base := normalize(c.Base())
	k := &Connector{c: c, auth: auth, base: base, key: hostKey(base), scope: s, prefixes: s.PathPrefixes, limits: limits}
	if len(k.prefixes) == 0 {
		k.prefixes = []string{base.Path}
	}
	return k, nil
}

// Recall takes the pages that sync stored. Changed then checks them with
// HEAD requests in place of a crawl.
func (k *Connector) Recall(_ connectors.Part, known []connectors.Known) { k.known = known }

// Skipped lists the pages that the crawl did not read, with the reason.
func (k *Connector) Skipped() []connectors.Skipped {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.skipped)
}

func (k *Connector) skip(id, reason string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.skipped = append(k.skipped, connectors.Skipped{ID: id, Reason: reason})
}

// Validate reads the seed and counts its pages. The user is the title of
// the site.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	if err := k.loadRobots(ctx); err != nil {
		return connectors.Validation{}, err
	}
	if k.scope.Seed == SeedLLMsFull {
		items, _, err := k.fullItems(ctx)
		if err != nil {
			return connectors.Validation{}, err
		}
		title := k.base.Host
		if len(items) > 0 {
			title = items[0].Title
		}
		return connectors.Validation{User: title, Sample: len(items)}, nil
	}
	title, seeds, err := k.seeds(ctx)
	if err != nil {
		return connectors.Validation{}, err
	}
	if title == "" && len(seeds) > 0 {
		p, err := k.page(ctx, seeds[0])
		var skip *connectors.SkipError
		switch {
		case errors.As(err, &skip):
		case err != nil:
			return connectors.Validation{}, err
		default:
			title = p.item.Title
		}
	}
	if title == "" {
		title = k.base.Host
	}
	return connectors.Validation{User: title, Sample: len(seeds)}, nil
}

// Scopes has no options: the scope is the seed and the path prefixes.
func (k *Connector) Scopes(context.Context) ([]connectors.Scope, error) {
	return []connectors.Scope{}, nil
}

// Parts is the site as one part.
func (k *Connector) Parts(context.Context) ([]connectors.Part, error) {
	return []connectors.Part{{Key: SitePart}}, nil
}

// Changed crawls the site from the seed when sync stored no page yet.
// Otherwise it sends HEAD for each stored page and lists it with its
// revision, so that sync reads only the pages whose revision changed. New
// pages come with the next reconciliation.
func (k *Connector) Changed(ctx context.Context, _ connectors.Part, _ time.Time, fn func(connectors.Ref) error) error {
	if err := k.loadRobots(ctx); err != nil {
		return err
	}
	if k.scope.Seed == SeedLLMsFull {
		return k.changedFull(ctx, fn)
	}
	if len(k.known) == 0 {
		return k.crawl(ctx, func(p *crawled) error {
			return fn(connectors.Ref{ID: p.item.ExternalID, Updated: p.item.Updated, Version: p.item.Revision, Item: p.item})
		})
	}
	for _, kn := range k.known {
		r, err := k.check(ctx, kn)
		if err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// check sends HEAD for a stored page. The ref has the revision that Fetch
// would store, or no revision when Fetch must decide.
func (k *Connector) check(ctx context.Context, kn connectors.Known) (connectors.Ref, error) {
	r := connectors.Ref{ID: kn.ID}
	u := parsed(kn.ID)
	if u == nil || !k.inScope(u) {
		// Fetch tombstones the page.
		return r, nil
	}
	if !k.robots.allowed(u.EscapedPath()) {
		r.Skip = ReasonRobots
		return r, nil
	}
	if kn.Revision == "" {
		return r, nil
	}
	if strings.HasPrefix(kn.Revision, revMarkdown) {
		if v := variant(u); v != nil && k.robots.allowed(v.EscapedPath()) {
			resp, err := k.c.Send(ctx, http.MethodHead, v.String(), acceptMarkdown, 0)
			switch {
			case err == nil && !isHTML(resp.Header):
				r.Version = revision(revMarkdown, resp.Header)
				return r, nil
			case err != nil && k.fatal(ctx, err):
				return r, err
			}
		}
	}
	resp, err := k.c.Send(ctx, http.MethodHead, u.String(), acceptPage, 0)
	switch {
	case errors.Is(err, outbound.ErrForeignHost):
		r.Skip = ReasonForeignRedirect
	case err != nil && k.fatal(ctx, err):
		return r, err
	case err == nil:
		r.Version = revision(revPage, resp.Header)
	}
	return r, nil
}

// fatal reports an error that ends the run: a refused token, a cancel, a
// failed rate reservation or a server error. A 404 or another client
// error of one page is not.
func (k *Connector) fatal(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, connectors.ErrNotFound) || errors.Is(err, outbound.ErrForeignHost) {
		return false
	}
	var se *connectors.StatusError
	if errors.Is(err, connectors.ErrUnauthorized) {
		return k.auth || !errors.As(err, &se) || se.Status != http.StatusForbidden
	}
	if errors.As(err, &se) {
		return se.RateLimit || se.Status >= 500
	}
	return true
}

// ListIDs crawls the site from the seed and lists the pages it reaches.
func (k *Connector) ListIDs(ctx context.Context, _ connectors.Part, fn func(string) error) error {
	if err := k.loadRobots(ctx); err != nil {
		return err
	}
	if k.scope.Seed == SeedLLMsFull {
		items, _, err := k.fullItems(ctx)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := fn(it.ExternalID); err != nil {
				return err
			}
		}
		return nil
	}
	return k.crawl(ctx, func(p *crawled) error { return fn(p.item.ExternalID) })
}

// Fetch reads one page. A page that returns 404 or 410, or that is outside
// the scope, returns ErrNotFound.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	if err := k.loadRobots(ctx); err != nil {
		return nil, err
	}
	if k.scope.Seed == SeedLLMsFull {
		items, _, err := k.fullItems(ctx)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.ExternalID == id {
				return it, nil
			}
		}
		return nil, connectors.ErrNotFound
	}
	u := parsed(id)
	if u == nil || !k.inScope(u) {
		return nil, connectors.ErrNotFound
	}
	p, err := k.page(ctx, u)
	if err != nil {
		return nil, err
	}
	p.item.ExternalID, p.item.URL = id, id
	return p.item, nil
}

// loadRobots reads /robots.txt once per run. A missing or refused file
// allows every path. A server error ends the run, so that no page is
// tombstoned for it.
func (k *Connector) loadRobots(ctx context.Context) error {
	k.robotsOnce.Do(func() {
		u := url.URL{Scheme: k.base.Scheme, Host: k.base.Host, Path: "/robots.txt"}
		resp, err := k.c.Send(ctx, http.MethodGet, u.String(), "text/plain", 512<<10)
		var se *connectors.StatusError
		switch {
		case err == nil:
			k.robots = parseRobots(resp.Body)
		case errors.Is(err, connectors.ErrNotFound), errors.Is(err, connectors.ErrUnauthorized), errors.Is(err, outbound.ErrForeignHost):
			k.robots = &robots{}
		case errors.As(err, &se) && !se.RateLimit && se.Status < 500:
			k.robots = &robots{}
		default:
			k.robotsErr = fmt.Errorf("web: read robots.txt: %w", err)
			return
		}
		if k.robots.delay > 0 {
			k.c.Slow(k.robots.delay)
		}
	})
	return k.robotsErr
}

// crawled is one page that the crawl read.
type crawled struct {
	item  *content.Item
	links []*url.URL
}

// crawl reads the pages breadth first from the seed and calls fn for each
// page it stores. A link counts once, after the fragment and the query are
// dropped.
func (k *Connector) crawl(ctx context.Context, fn func(*crawled) error) error {
	_, seeds, err := k.seeds(ctx)
	if err != nil {
		return err
	}
	type entry struct {
		u     *url.URL
		depth int
	}
	seen := map[string]bool{}
	var queue []entry
	add := func(u *url.URL, depth int) {
		n := normalize(u)
		id := n.String()
		if seen[id] || !k.inScope(n) || !isPageLike(n) {
			return
		}
		seen[id] = true
		queue = append(queue, entry{n, depth})
	}
	for _, s := range seeds {
		add(s, 0)
	}
	stored := 0
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		if stored >= k.scope.MaxPages {
			k.skip(k.base.String(), ReasonPageLimit)
			break
		}
		p, err := k.page(ctx, e.u)
		var skip *connectors.SkipError
		switch {
		case errors.As(err, &skip):
			k.skip(e.u.String(), skip.Reason)
			continue
		case errors.Is(err, connectors.ErrNotFound):
			continue
		case err != nil:
			return err
		}
		if final := p.item.ExternalID; final != e.u.String() {
			// A redirect on the same host: the page is its target, read
			// once.
			if seen[final] {
				continue
			}
			seen[final] = true
		}
		stored++
		if err := fn(p); err != nil {
			return err
		}
		if e.depth >= *k.scope.MaxDepth {
			continue
		}
		for _, l := range p.links {
			add(l, e.depth+1)
		}
	}
	if stored == 0 {
		// A site that is down or a seed that moved would otherwise
		// tombstone every page.
		return ErrNoPage
	}
	return nil
}

// seeds returns the title of the site and the seed pages.
func (k *Connector) seeds(ctx context.Context) (string, []*url.URL, error) {
	order := []string{k.scope.Seed}
	if k.scope.Seed == SeedAuto {
		order = []string{SeedLLMs, SeedSitemap, SeedStart}
	}
	for _, seed := range order {
		var title string
		var pages []*url.URL
		var err error
		switch seed {
		case SeedLLMs:
			title, pages, err = k.llms(ctx)
		case SeedSitemap:
			pages, err = k.sitemap(ctx)
		case SeedStart:
			return "", []*url.URL{k.base}, nil
		}
		if err != nil {
			return "", nil, err
		}
		var in []*url.URL
		for _, p := range pages {
			if n := normalize(p); k.inScope(n) {
				in = append(in, n)
			}
		}
		if len(in) > 0 || k.scope.Seed != SeedAuto {
			return title, in, nil
		}
	}
	return "", nil, nil
}

// file returns the URL of a file below the base URL, such as llms.txt.
func (k *Connector) file(name string) *url.URL {
	u := *k.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + name
	return &u
}

// text reads a seed file. A missing file, or an HTML page in its place,
// returns nil.
func (k *Connector) text(ctx context.Context, u *url.URL, accept string) (*connectors.Response, error) {
	if !k.robots.allowed(u.EscapedPath()) {
		return nil, nil
	}
	resp, err := k.c.Send(ctx, http.MethodGet, u.String(), accept, 0)
	if errors.Is(err, connectors.ErrNotFound) || isGone(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if isHTML(resp.Header) {
		return nil, nil
	}
	return resp, nil
}

func (k *Connector) llms(ctx context.Context) (string, []*url.URL, error) {
	u := k.file("llms.txt")
	resp, err := k.text(ctx, u, "text/plain, text/markdown")
	if err != nil || resp == nil {
		return "", nil, err
	}
	title, pages := parseLLMs(resp.Body, resp.URL)
	return title, pages, nil
}

func (k *Connector) sitemap(ctx context.Context) ([]*url.URL, error) {
	files := []*url.URL{k.file("sitemap.xml")}
	seen := map[string]bool{}
	var out []*url.URL
	for read := 0; len(files) > 0 && read < maxSitemaps; read++ {
		f := files[0]
		files = files[1:]
		if seen[f.String()] || hostKey(f) != k.key {
			continue
		}
		seen[f.String()] = true
		resp, err := k.text(ctx, f, "application/xml, text/xml")
		if err != nil {
			return nil, err
		}
		if resp == nil {
			continue
		}
		pages, children, err := parseSitemap(resp.Body, resp.URL)
		if err != nil {
			return nil, err
		}
		out = append(out, pages...)
		files = append(files, children...)
		if len(out) >= maxPagesLimit {
			break
		}
	}
	return out, nil
}

// The prefixes of a revision say which URL it is for: the Markdown
// variant or the page.
const (
	revMarkdown = "md:"
	revPage     = "page:"
	revFull     = "full:"
)

// revision is the ETag, else Last-Modified, of an answer. Empty means that
// the content hash decides.
func revision(prefix string, h http.Header) string {
	tag := h.Get("ETag")
	if tag == "" {
		tag = h.Get("Last-Modified")
	}
	if tag == "" {
		return ""
	}
	return prefix + tag
}

const (
	acceptMarkdown = "text/markdown, text/plain;q=0.9"
	acceptPage     = "text/html, application/xhtml+xml, text/markdown;q=0.9, text/plain;q=0.5"
)

// page reads one page: the Markdown variant when the site has one, else
// the HTML converted to Markdown. The item ID is the URL of the page after
// redirects on the same host.
func (k *Connector) page(ctx context.Context, u *url.URL) (*crawled, error) {
	if !k.robots.allowed(u.EscapedPath()) {
		return nil, &connectors.SkipError{Reason: ReasonRobots}
	}
	if v := variant(u); v != nil && k.robots.allowed(v.EscapedPath()) {
		p, err := k.markdownVariant(ctx, u, v)
		if p != nil || err != nil {
			return p, err
		}
	}
	resp, err := k.c.Send(ctx, http.MethodGet, u.String(), acceptPage, k.limits.MaxItemBytes)
	if err != nil {
		return nil, k.pageError(ctx, err)
	}
	if resp.TooLarge {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	final := normalize(resp.URL)
	if !k.inScope(final) {
		return nil, connectors.ErrNotFound
	}
	mt := mediaType(resp.Header)
	var title, body string
	var links []*url.URL
	switch {
	case mt == "text/html" || mt == "application/xhtml+xml":
		if title, body, links, err = fromHTML(resp.Body, resp.URL); err != nil {
			return nil, err
		}
	case mt == "text/markdown" || mt == "text/x-markdown" || (mt == "text/plain" && strings.HasSuffix(final.Path, ".md")):
		title, body, links = fromMarkdown(string(resp.Body), resp.URL)
	default:
		return nil, &connectors.SkipError{Reason: ReasonNotPage}
	}
	return &crawled{item: k.item(final, title, body, revision(revPage, resp.Header), resp.Header, "html"), links: links}, nil
}

// markdownVariant sends HEAD to the Markdown variant of a page and reads
// it on 200. It returns nil when the page has no variant.
func (k *Connector) markdownVariant(ctx context.Context, u, v *url.URL) (*crawled, error) {
	head, err := k.c.Send(ctx, http.MethodHead, v.String(), acceptMarkdown, 0)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	if isHTML(head.Header) {
		return nil, nil
	}
	resp, err := k.c.Send(ctx, http.MethodGet, v.String(), acceptMarkdown, k.limits.MaxItemBytes)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	if resp.TooLarge {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	if isHTML(resp.Header) {
		return nil, nil
	}
	title, body, links := fromMarkdown(string(resp.Body), u)
	return &crawled{item: k.item(u, title, body, revision(revMarkdown, resp.Header), resp.Header, "markdown"), links: links}, nil
}

// pageError maps the error of a page request.
func (k *Connector) pageError(ctx context.Context, err error) error {
	var se *connectors.StatusError
	switch {
	case isGone(err):
		return connectors.ErrNotFound
	case errors.Is(err, outbound.ErrForeignHost):
		return &connectors.SkipError{Reason: ReasonForeignRedirect}
	case !k.fatal(ctx, err) && errors.As(err, &se):
		return &connectors.SkipError{Reason: fmt.Sprintf("The site answers %d for the page.", se.Status)}
	}
	return err
}

func (k *Connector) item(u *url.URL, title, body, rev string, h http.Header, source string) *content.Item {
	if title == "" {
		title = path.Base(strings.TrimRight(u.Path, "/"))
		if title == "" || title == "." || title == "/" {
			title = u.Host
		}
	}
	it := &content.Item{ExternalID: u.String(), Kind: "page", Title: title, URL: u.String(), Body: body, Revision: rev,
		Metadata: map[string]any{"site": u.Host, "source": source}}
	if t, err := http.ParseTime(h.Get("Last-Modified")); err == nil {
		it.Updated = t.UTC()
	}
	return it
}

// changedFull lists the sections of llms-full.txt. When sync stored the
// sections of the same file revision, a HEAD request is enough.
func (k *Connector) changedFull(ctx context.Context, fn func(connectors.Ref) error) error {
	if len(k.known) > 0 {
		u := k.file("llms-full.txt")
		if k.robots.allowed(u.EscapedPath()) {
			head, err := k.c.Send(ctx, http.MethodHead, u.String(), "text/plain, text/markdown", 0)
			if err != nil && k.fatal(ctx, err) {
				return err
			}
			if err == nil {
				rev := revision(revFull, head.Header)
				if rev != "" && !slices.ContainsFunc(k.known, func(kn connectors.Known) bool { return kn.Revision != rev }) {
					for _, kn := range k.known {
						if err := fn(connectors.Ref{ID: kn.ID, Version: rev}); err != nil {
							return err
						}
					}
					return nil
				}
			}
		}
	}
	items, _, err := k.fullItems(ctx)
	if err != nil {
		return err
	}
	for _, it := range items {
		r := connectors.Ref{ID: it.ExternalID, Updated: it.Updated, Version: it.Revision, Item: it}
		if k.limits.MaxItemBytes > 0 && len(it.Body) > k.limits.MaxItemBytes {
			r.Item, r.Skip = nil, "too_large"
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// fullItems reads llms-full.txt once per run. Each top-level section is
// an item. Its URL is the first link of the section when it is on the same
// host, else the file URL with the slug of the section.
func (k *Connector) fullItems(ctx context.Context) ([]*content.Item, string, error) {
	k.fullOnce.Do(func() {
		u := k.file("llms-full.txt")
		resp, err := k.text(ctx, u, "text/plain, text/markdown")
		if err != nil {
			k.fullErr = err
			return
		}
		if resp == nil {
			k.fullErr = fmt.Errorf("web: %s does not exist", u)
			return
		}
		k.fullRev = revision(revFull, resp.Header)
		taken := map[string]bool{}
		for _, s := range splitFull(resp.Body, resp.URL) {
			id := ""
			if s.Link != nil && hostKey(s.Link) == k.key {
				id = normalize(s.Link).String()
			}
			if id == "" || taken[id] {
				f := *u
				f.Fragment = s.Slug
				id = f.String()
			}
			taken[id] = true
			_, body, _ := fromMarkdown(s.Body, resp.URL)
			it := k.item(u, s.Title, body, k.fullRev, resp.Header, "llms-full")
			it.ExternalID, it.URL = id, id
			k.full = append(k.full, it)
		}
	})
	return k.full, k.fullRev, k.fullErr
}

// inScope reports whether a URL is on the scheme and host of the base URL
// and inside a path prefix. The seed files are not pages.
func (k *Connector) inScope(u *url.URL) bool {
	if hostKey(u) != k.key {
		return false
	}
	switch path.Base(u.Path) {
	case "llms.txt", "llms-full.txt", "sitemap.xml", "robots.txt":
		return false
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	for _, pre := range k.prefixes {
		trimmed := strings.TrimRight(pre, "/")
		if trimmed == "" || p == trimmed || strings.HasPrefix(p, trimmed+"/") {
			return true
		}
	}
	return false
}

// files are the extensions of links that are not pages. The crawl does not
// request them.
var files = map[string]bool{
	".pdf": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true, ".ico": true,
	".css": true, ".js": true, ".mjs": true, ".json": true, ".xml": true, ".zip": true, ".gz": true, ".tgz": true,
	".tar": true, ".mp3": true, ".mp4": true, ".webm": true, ".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".csv": true, ".xls": true, ".xlsx": true, ".doc": true, ".docx": true, ".ppt": true, ".pptx": true, ".exe": true,
	".dmg": true, ".msi": true, ".wasm": true,
}

func isPageLike(u *url.URL) bool { return !files[strings.ToLower(path.Ext(u.Path))] }

// variant is the Markdown variant of a page: the URL with .md added, or
// index.html.md for a URL that ends in /. A Markdown URL has none.
func variant(u *url.URL) *url.URL {
	if strings.HasSuffix(strings.ToLower(u.Path), ".md") {
		return nil
	}
	v := *u
	if v.Path == "" || strings.HasSuffix(v.Path, "/") {
		v.Path = strings.TrimSuffix(v.Path, "/") + "/index.html.md"
	} else {
		v.Path += ".md"
	}
	v.RawPath = ""
	return &v
}

// parsed returns the URL of an item ID, or nil.
func parsed(id string) *url.URL {
	u, err := url.Parse(id)
	if err != nil {
		return nil
	}
	return u
}

func mediaType(h http.Header) string {
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return strings.ToLower(mt)
}

func isHTML(h http.Header) bool {
	mt := mediaType(h)
	return mt == "text/html" || mt == "application/xhtml+xml"
}

func isGone(err error) bool {
	var se *connectors.StatusError
	return errors.As(err, &se) && se.Status == http.StatusGone
}

// normalize is the identity of a page URL: the lowercase scheme and host
// without the default port, the path with / for an empty path, and no
// fragment, query or user.
func normalize(u *url.URL) *url.URL {
	n := *u
	n.Scheme = strings.ToLower(n.Scheme)
	host, port := strings.ToLower(n.Hostname()), n.Port()
	if (n.Scheme == "http" && port == "80") || (n.Scheme == "https" && port == "443") {
		port = ""
	}
	n.Host = host
	if port != "" {
		n.Host = host + ":" + port
	}
	if strings.Contains(host, ":") {
		n.Host = "[" + host + "]"
		if port != "" {
			n.Host += ":" + port
		}
	}
	n.User, n.Fragment, n.RawFragment, n.RawQuery, n.ForceQuery, n.Opaque = nil, "", "", "", false, ""
	if n.Path == "" {
		n.Path = "/"
		n.RawPath = ""
	}
	return &n
}

// hostKey is the scheme with the host and its effective port.
func hostKey(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}
