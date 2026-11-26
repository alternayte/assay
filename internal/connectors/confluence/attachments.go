package confluence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/content/files"
	"github.com/alternayte/assay/internal/outbound"
)

// attachmentPrefix starts the item ID of an attachment: att:<attachment ID>.
const attachmentPrefix = "att:"

// attachmentExpand is the expansion of an attachment of REST v1.
const attachmentExpand = "container,version,metadata"

// Recall records the stored pages of a part. An attachment is read only
// when its page is a stored item of the connection or a page that this run
// lists.
func (k *Connector) Recall(part connectors.Part, known []connectors.Known) {
	for _, it := range known {
		if !strings.HasPrefix(it.ID, attachmentPrefix) {
			k.addPage(part.Key, it.ID)
		}
	}
}

func (k *Connector) addPage(part, id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pages[part] == nil {
		k.pages[part] = map[string]bool{}
	}
	k.pages[part][id] = true
}

func (k *Connector) hasPage(part, id string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.pages[part][id]
}

// attachmentCQL is the query of the attachments of the spaces of a part.
// The scope applies to attachments through their page.
func attachmentCQL(space string) string {
	return "type=attachment AND space in (" + quoted([]string{space}) + ")"
}

// fileType is the type of an attachment when the settings read it, or "".
func (k *Connector) fileType(a *pageJSON) string {
	mt := a.Extensions.MediaType
	if mt == "" {
		mt = a.Metadata.MediaType
	}
	t := files.TypeOf(a.Title, mt)
	if t == "" || !slices.Contains(k.rules.AttachmentTypes, t) {
		return ""
	}
	return t
}

// onPage reports an attachment of a page, not of a blog post.
func onPage(a *pageJSON) bool {
	return a.Container.ID != "" && (a.Container.Type == "" || a.Container.Type == "page")
}

// changedAttachments lists the attachments of a part modified at or after
// since whose page the run reads. One CQL query finds the changed
// attachments of all pages. A file larger than the limit is skipped with
// too_large before any download.
func (k *Connector) changedAttachments(ctx context.Context, part connectors.Part, since time.Time, fn func(connectors.Ref) error) error {
	if len(k.rules.AttachmentTypes) == 0 {
		return nil
	}
	cql := attachmentCQL(part.Key)
	if !since.IsZero() {
		cql += fmt.Sprintf(` AND lastmodified >= "%s"`, since.Add(-connectors.Overlap).UTC().Format("2006/01/02 15:04"))
	}
	cql += " order by lastmodified asc"
	return k.search(ctx, cql, attachmentExpand, 100, func(a *pageJSON) error {
		if !onPage(a) || !k.hasPage(part.Key, a.Container.ID) || k.fileType(a) == "" {
			return nil
		}
		t, _ := time.Parse(time.RFC3339, a.Version.When)
		r := connectors.Ref{ID: attachmentPrefix + a.ID, Updated: t, Version: revision(a.Version.Number)}
		if k.rules.MaxAttachmentBytes > 0 && a.Extensions.FileSize > int64(k.rules.MaxAttachmentBytes) {
			r.Skip = "too_large"
		}
		return fn(r)
	})
}

// listAttachments lists the IDs of the attachments of the pages of a part
// that the settings read.
func (k *Connector) listAttachments(ctx context.Context, part connectors.Part, pages map[string]bool, fn func(string) error) error {
	if len(k.rules.AttachmentTypes) == 0 {
		return nil
	}
	return k.search(ctx, attachmentCQL(part.Key), attachmentExpand, 100, func(a *pageJSON) error {
		if !onPage(a) || !pages[a.Container.ID] || k.fileType(a) == "" {
			return nil
		}
		return fn(attachmentPrefix + a.ID)
	})
}

// attachmentV2 is an attachment of REST v2.
type attachmentV2 struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	PageID       string `json:"pageId"`
	MediaType    string `json:"mediaType"`
	FileSize     int64  `json:"fileSize"`
	CreatedAt    string `json:"createdAt"`
	WebUILink    string `json:"webuiLink"`
	DownloadLink string `json:"downloadLink"`
	Version      struct {
		Number    int    `json:"number"`
		CreatedAt string `json:"createdAt"`
		AuthorID  string `json:"authorId"`
	} `json:"version"`
	Links struct {
		WebUI    string `json:"webui"`
		Download string `json:"download"`
	} `json:"_links"`
}

// attachment reads one attachment as REST v1 shows it. Cloud reads it
// through REST v2 and names the page and the author by ID.
func (k *Connector) attachment(ctx context.Context, id string) (*pageJSON, error) {
	var a pageJSON
	if !k.cloud {
		if err := k.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id), url.Values{"expand": {attachmentExpand}}, &a); err != nil {
			return nil, err
		}
		return &a, nil
	}
	var v attachmentV2
	if err := k.c.GetJSON(ctx, "/wiki/api/v2/attachments/"+url.PathEscape(id), nil, &v); err != nil {
		return nil, err
	}
	a.ID, a.Title = v.ID, v.Title
	a.Container.ID, a.Container.Type = v.PageID, "page"
	a.Extensions.MediaType, a.Extensions.FileSize = v.MediaType, v.FileSize
	a.Version.Number, a.Version.When = v.Version.Number, v.Version.CreatedAt
	a.History.CreatedDate = v.CreatedAt
	a.Links.WebUI, a.Links.Download = v.WebUILink, v.DownloadLink
	if a.Links.WebUI == "" {
		a.Links.WebUI = v.Links.WebUI
	}
	if a.Links.Download == "" {
		a.Links.Download = v.Links.Download
	}
	if v.PageID == "" {
		return &a, nil
	}
	var err error
	if a.Container.Title, err = k.title(ctx, v.PageID); err != nil {
		return nil, err
	}
	if a.Version.By.DisplayName, err = k.user(ctx, v.Version.AuthorID); err != nil {
		return nil, err
	}
	return &a, nil
}

// fetchAttachment reads one attachment and the text of its file. The file
// size of the attachment is checked before the download. The download is a
// GET of the download link on the connection host; a Cloud download
// redirects to the media host of the client.
func (k *Connector) fetchAttachment(ctx context.Context, id string) (*content.Item, error) {
	a, err := k.attachment(ctx, id)
	if err != nil {
		return nil, err
	}
	typ := k.fileType(a)
	if !onPage(a) || typ == "" {
		// The listing names only the attachments of pages with a type
		// that the settings read. Any other attachment is not read, and a
		// stored version goes.
		return nil, connectors.ErrNotFound
	}
	if k.rules.MaxAttachmentBytes > 0 && a.Extensions.FileSize > int64(k.rules.MaxAttachmentBytes) {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	link, err := url.Parse(a.Links.Download)
	if err != nil || a.Links.Download == "" {
		return nil, fmt.Errorf("confluence: attachment %s has no valid download link %q", id, a.Links.Download)
	}
	resp, err := k.c.Send(ctx, http.MethodGet, k.c.URL(k.prefix()+link.EscapedPath(), link.Query()), "*/*", k.rules.MaxAttachmentBytes)
	switch {
	case errors.Is(err, outbound.ErrForeignHost):
		// A redirect to a host that the client does not send to.
		return nil, &connectors.SkipError{Reason: "foreign_host"}
	case err != nil:
		return nil, err
	case resp.TooLarge:
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	body, err := files.Markdown(typ, resp.Body)
	if err != nil {
		return nil, &connectors.SkipError{Reason: files.Reason(err)}
	}
	// The heading path of each passage starts with the page title, so
	// that a result shows the page that holds the file.
	ps := content.Split(body)
	if t := content.Normalize(a.Container.Title); t != "" {
		for i := range ps {
			ps[i].HeadingPath = append([]string{t}, ps[i].HeadingPath...)
		}
	}
	updated, _ := time.Parse(time.RFC3339, a.Version.When)
	created, err := time.Parse(time.RFC3339, a.History.CreatedDate)
	if err != nil {
		created = updated
	}
	mt := a.Extensions.MediaType
	if mt == "" {
		mt = a.Metadata.MediaType
	}
	it := &content.Item{
		ExternalID: attachmentPrefix + a.ID, Kind: "file", Title: a.Title,
		URL:    k.c.Link(k.prefix()+link.Path, nil),
		Author: a.Version.By.name(), EditedBy: a.Version.By.name(),
		Created: created, Updated: updated, Body: body, Passages: ps, Revision: revision(a.Version.Number),
		Metadata: map[string]any{"parent": a.Container.ID, "parentTitle": a.Container.Title, "mediaType": mt, "fileSize": a.Extensions.FileSize},
	}
	if web, err := url.Parse(a.Links.WebUI); err == nil && a.Links.WebUI != "" {
		it.URL = k.c.Link(k.prefix()+web.Path, web.Query())
	}
	it.Entities = connectors.Acronyms(body, "confluence", k.c.Host())
	return it, nil
}
