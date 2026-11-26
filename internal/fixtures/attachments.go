package fixtures

import (
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alternayte/assay/internal/content/files"
)

// Attachment is a file attached to a Confluence page of the corpus.
type Attachment struct {
	ID        string
	Page      string
	Title     string
	MediaType string
	Author    string
	Updated   time.Time
	Version   int
	Data      []byte
}

// scans are the corpus attachments that a sync skips: the reason by ID.
var scans = map[string]string{"att5004": "no_text"}

// AttachmentItems is the number of attachments of the pages of a space
// that a sync with the default settings stores: a PDF, Word or draw.io
// file with text.
func (s *Sources) AttachmentItems(space string) int {
	n := 0
	for _, a := range s.spaceAttachments(space) {
		if scans[a.ID] == "" {
			n++
		}
	}
	return n
}

// AttachmentSkips is the number of attachments of the pages of a space
// that a sync with the default settings skips.
func (s *Sources) AttachmentSkips(space string) int {
	return len(s.spaceAttachments(space)) - s.AttachmentItems(space)
}

// spaceAttachments are the attachments of a type that Assay reads on the
// pages of a space.
func (s *Sources) spaceAttachments(space string) []*Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Attachment
	for _, a := range s.attachments {
		if pg, ok := s.pages[a.Page]; ok && pg.Space == space && files.TypeOf(a.Title, a.MediaType) != "" {
			out = append(out, a)
		}
	}
	return out
}

// MediaPath is the path of the Atlassian media host on the fixture server.
// A Cloud download redirects to it on the other host name of the fixture
// server: localhost for 127.0.0.1 and 127.0.0.1 for localhost.
const MediaPath = "/media"

// CloudAttachmentID is the Cloud ID of an attachment. A migration to Cloud
// gives every attachment a new ID.
func CloudAttachmentID(id string) string { return "att33" + strings.TrimPrefix(id, "att") }

func attachmentID(id string, cloud bool) string {
	if cloud {
		return CloudAttachmentID(id)
	}
	return id
}

// loadAttachments reads corpus/confluence/attachments.txt: the ID, the
// page, the author, the update time, the media type and the file name. The
// file is corpus/confluence/files/<ID>.
func (s *Sources) loadAttachments() {
	for _, l := range lines("corpus/confluence/attachments.txt") {
		f := strings.SplitN(l, " ", 6)
		if len(f) != 6 {
			panic("fixtures: bad attachment line " + l)
		}
		b, err := corpus.ReadFile("corpus/confluence/files/" + f[0])
		if err != nil {
			panic(err)
		}
		s.attachments[f[0]] = &Attachment{ID: f[0], Page: f[1], Author: f[2], Updated: when(f[3]), MediaType: f[4], Title: f[5],
			Version: 1, Data: b}
	}
}

// PutAttachment adds or changes an attachment. A change raises the version
// and the update time.
func (s *Sources) PutAttachment(a Attachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.attachments[a.ID]; ok {
		a.Version = old.Version + 1
	} else if a.Version == 0 {
		a.Version = 1
	}
	if a.Updated.IsZero() {
		a.Updated = s.now().UTC().Truncate(time.Second)
	}
	s.attachments[a.ID] = &a
}

// Attachment returns a copy of an attachment.
func (s *Sources) Attachment(id string) (Attachment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attachments[id]
	if !ok {
		return Attachment{}, false
	}
	return *a, true
}

// DeleteAttachment removes an attachment.
func (s *Sources) DeleteAttachment(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attachments, id)
}

// attachmentByID returns the attachment of a Data Center or Cloud ID. The
// lock is held.
func (s *Sources) attachmentByID(id string, cloud bool) (*Attachment, bool) {
	if cloud {
		if !strings.HasPrefix(id, "att33") {
			return nil, false
		}
		id = "att" + strings.TrimPrefix(id, "att33")
	}
	a, ok := s.attachments[id]
	return a, ok
}

// downloadPath is the download link of an attachment, relative to the
// context path.
func downloadPath(a *Attachment, cloud bool) string {
	q := url.Values{"version": {strconv.Itoa(a.Version)}, "modificationDate": {strconv.FormatInt(a.Updated.UnixMilli(), 10)}, "api": {"v2"}}
	return "/download/attachments/" + pageID(a.Page, cloud) + "/" + url.PathEscape(a.Title) + "?" + q.Encode()
}

func attachmentWebUI(a *Attachment, cloud bool) string {
	q := url.Values{"pageId": {pageID(a.Page, cloud)}, "preview": {"/" + pageID(a.Page, cloud) + "/" + attachmentID(a.ID, cloud) + "/" + a.Title}}
	return "/pages/viewpageattachments.action?" + q.Encode()
}

// attachmentJSON is an attachment of REST v1 with its container, version
// and extensions. The lock is held.
func (s *Sources) attachmentJSON(a *Attachment, cloud bool) map[string]any {
	person := s.person
	if cloud {
		person = s.confluencePerson
	}
	container := map[string]any{"id": pageID(a.Page, cloud), "type": "page", "status": "current"}
	space := map[string]any{}
	if pg, ok := s.pages[a.Page]; ok {
		container["title"] = pg.Title
		space["key"] = pg.Space
	}
	return map[string]any{
		"id": attachmentID(a.ID, cloud), "type": "attachment", "status": "current", "title": a.Title,
		"version":    map[string]any{"number": a.Version, "when": a.Updated.Format(confluenceTime), "by": person(a.Author)},
		"container":  container,
		"space":      space,
		"metadata":   map[string]any{"mediaType": a.MediaType},
		"extensions": map[string]any{"mediaType": a.MediaType, "fileSize": len(a.Data), "comment": ""},
		"_links":     map[string]any{"webui": attachmentWebUI(a, cloud), "download": downloadPath(a, cloud)},
	}
}

// attachmentV2 is an attachment of REST v2. The lock is held.
func attachmentV2(a *Attachment) map[string]any {
	return map[string]any{
		"id": CloudAttachmentID(a.ID), "status": "current", "title": a.Title, "createdAt": a.Updated.Format(confluenceTime),
		"pageId": CloudPageID(a.Page), "mediaType": a.MediaType, "comment": "", "fileId": "f-" + a.ID, "fileSize": len(a.Data),
		"webuiLink": attachmentWebUI(a, true), "downloadLink": downloadPath(a, true),
		"version": map[string]any{"number": a.Version, "createdAt": a.Updated.Format(confluenceTime), "authorId": AccountID(a.Author)},
		"_links":  map[string]any{"webui": attachmentWebUI(a, true), "download": downloadPath(a, true), "base": "http://ignored.acme.test/wiki"},
	}
}

// searchAttachments answers a CQL search of type attachment. The space and
// the change time apply to the attachment, which is in the space of its
// page. The lock is held.
func (s *Sources) searchAttachments(w http.ResponseWriter, r *http.Request, keep func(*Page) bool, cloud bool) {
	var all []*Attachment
	for _, a := range s.attachments {
		pg, ok := s.pages[a.Page]
		if !ok {
			continue
		}
		// The filter reads the space of the page and the change time of
		// the attachment. An attachment has no labels.
		probe := &Page{ID: pg.ID, Space: pg.Space, Parent: pg.Parent, Updated: a.Updated}
		if keep(probe) {
			all = append(all, a)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].Updated.Equal(all[j].Updated) {
			return all[i].Updated.Before(all[j].Updated)
		}
		return all[i].ID < all[j].ID
	})
	start, limit := page(r, 25)
	res := []map[string]any{}
	for _, a := range window(all, start, limit) {
		res = append(res, s.attachmentJSON(a, cloud))
	}
	links := map[string]any{"base": "http://ignored.acme.test/confluence", "context": "/confluence"}
	if cloud {
		links = map[string]any{"base": "http://ignored.acme.test/wiki", "context": "/wiki"}
	}
	if start+len(res) < len(all) && len(res) > 0 {
		q := r.URL.Query()
		q.Set("start", strconv.Itoa(start+len(res)))
		q.Set("limit", strconv.Itoa(limit))
		links["next"] = "/rest/api/content/search?" + q.Encode()
	}
	out := map[string]any{"results": res, "start": start, "limit": limit, "size": len(res), "_links": links}
	if !cloud {
		out["totalSize"] = len(all)
	}
	writeJSON(w, http.StatusOK, out)
}

// download answers the download of an attachment by page ID and file name.
// The lock is held.
func (s *Sources) download(w http.ResponseWriter, r *http.Request, p string, cloud bool) {
	rest := strings.TrimPrefix(p, "/download/attachments/")
	page, name, _ := strings.Cut(rest, "/")
	if cloud {
		page = strings.TrimPrefix(page, "33")
	}
	for _, a := range s.attachments {
		if a.Page != page || a.Title != name {
			continue
		}
		if cloud {
			// Cloud sends the file from the media host.
			host := r.Host
			switch {
			case strings.HasPrefix(host, "127.0.0.1"):
				host = "localhost" + strings.TrimPrefix(host, "127.0.0.1")
			case strings.HasPrefix(host, "localhost"):
				host = "127.0.0.1" + strings.TrimPrefix(host, "localhost")
			}
			target := "http://" + host + MediaPath + "/file/" + a.ID + "/binary?token=acme-media&name=" + url.QueryEscape(a.Title)
			http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // The fixture server redirects to itself on its other host name.
			return
		}
		w.Header().Set("Content-Type", a.MediaType)
		_, _ = w.Write(a.Data)
		return
	}
	http.NotFound(w, r)
}

// media answers a file of the media host. The redirect carries the token,
// so the request needs no Authorization header.
func (s *Sources) media(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := path.Base(path.Dir(strings.TrimPrefix(r.URL.Path, MediaPath)))
	a, ok := s.attachments[id]
	if !ok || r.URL.Query().Get("token") != "acme-media" || r.Header.Get("Authorization") != "" {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", a.MediaType)
	_, _ = w.Write(a.Data)
}
