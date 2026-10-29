package registry

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/visibility"
)

// VersionInfo is one saved version of a record.
type VersionInfo struct {
	ID       int64     `json:"id"`
	EditedBy string    `json:"editedBy"`
	At       time.Time `json:"at"`
}

// RecordView is a record with its versions, newest first.
type RecordView struct {
	Key string `json:"key"`
	RegistryRecord
	Versions []VersionInfo `json:"versions"`
}

// DiffLine is one line of a diff: op is "=", "+" or "-".
type DiffLine struct {
	Op   string `json:"op" enum:"=,+,-"`
	Text string `json:"text"`
}

// RecordSummary is one record of the list.
type RecordSummary struct {
	Key       string     `json:"key"`
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	Status    string     `json:"status,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// Get returns a record with its versions.
func (s *Service) Get(ctx context.Context, key string) (*RecordView, error) {
	q := New(s.Pool)
	reg, err := q.RegistryID(ctx)
	if err != nil {
		return nil, err
	}
	item, err := q.RecordItem(ctx, RecordItemParams{Visible: []int64{reg}, Key: key})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && item.VersionID == nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v, err := q.RecordVersion(ctx, RecordVersionParams{ItemID: item.ID, ID: *item.VersionID})
	if err != nil {
		return nil, err
	}
	rec, err := recordOf(v.Metadata)
	if err != nil {
		return nil, err
	}
	rows, err := q.RecordVersions(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	out := &RecordView{Key: key, RegistryRecord: *rec, Versions: []VersionInfo{}}
	for _, r := range rows {
		out.Versions = append(out.Versions, VersionInfo{ID: r.ID, EditedBy: r.EditedBy, At: r.CreatedAt})
	}
	return out, nil
}

// VersionDiff returns a version of a record and its line diff against the
// version before it.
func (s *Service) VersionDiff(ctx context.Context, key string, id int64) (*RegistryRecord, []DiffLine, error) {
	q := New(s.Pool)
	reg, err := q.RegistryID(ctx)
	if err != nil {
		return nil, nil, err
	}
	item, err := q.RecordItem(ctx, RecordItemParams{Visible: []int64{reg}, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	v, err := q.RecordVersion(ctx, RecordVersionParams{ItemID: item.ID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	rec, err := recordOf(v.Metadata)
	if err != nil {
		return nil, nil, err
	}
	before := ""
	prev, err := q.PreviousVersion(ctx, PreviousVersionParams{ItemID: item.ID, ID: id})
	switch {
	case err == nil:
		before = prev.Body
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, nil, err
	}
	return rec, Diff(before, v.Body), nil
}

// Diff is the line diff of two texts by the longest common subsequence.
func Diff(a, b string) []DiffLine {
	split := func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if strings.TrimSpace(l) != "" {
				out = append(out, l)
			}
		}
		return out
	}
	x, y := split(a), split(b)
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	out := []DiffLine{}
	i, j := 0, 0
	for i < len(x) && j < len(y) {
		switch {
		case x[i] == y[j]:
			out = append(out, DiffLine{Op: "=", Text: x[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{Op: "-", Text: x[i]})
			i++
		default:
			out = append(out, DiffLine{Op: "+", Text: y[j]})
			j++
		}
	}
	for ; i < len(x); i++ {
		out = append(out, DiffLine{Op: "-", Text: x[i]})
	}
	for ; j < len(y); j++ {
		out = append(out, DiffLine{Op: "+", Text: y[j]})
	}
	return out
}

// Revert saves the record of an old version as a new version.
func (s *Service) Revert(ctx context.Context, key string, id int64, ed Editor) (*SavedRecord, error) {
	rec, _, err := s.VersionDiff(ctx, key, id)
	if err != nil {
		return nil, err
	}
	return s.Save(ctx, key, *rec, ed)
}

func editor(ctx context.Context, vis visibility.Set) Editor {
	p := api.Caller(ctx)
	name := p.User.DisplayName
	if name == "" {
		name = p.User.Email
	}
	return Editor{UserID: p.User.ID, Name: name, Role: p.Role, Vis: vis}
}

func saveErr(err error) error {
	var inv *InvalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &inv):
		return api.Problem(http.StatusUnprocessableEntity, "invalid_record", inv.Msg)
	case errors.Is(err, ErrExists):
		return api.Problem(http.StatusConflict, "exists", "A record with this type and name exists.")
	case errors.Is(err, ErrNotFound):
		return api.ErrNotFound
	case errors.Is(err, ErrStatusRole):
		return api.Problem(http.StatusForbidden, "status_role", "Only reviewers approve and deprecate terms.")
	}
	return api.Internal(err)
}

type keyIn struct {
	Key string `path:"key" doc:"The record key, such as registry:service:card-gateway."`
}

func (k keyIn) key() string { return unescape(k.Key) }

func unescape(k string) string {
	if u, err := url.PathUnescape(k); err == nil {
		return u
	}
	return k
}

// Register adds the registry routes. Members read and write records.
func (s *Service) Register(a huma.API) {
	members := huma.Middlewares{api.Require(auth.RoleMember)}
	tags := []string{"registry"}

	type listIn struct {
		Kind string `query:"kind" enum:"thing,term,"`
	}
	type listOut struct {
		Body struct {
			Records []RecordSummary `json:"records"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listRecords", Method: http.MethodGet, Path: "/api/v1/registry/records", Tags: tags,
		Summary: "List registry records", Middlewares: members,
	}, func(ctx context.Context, in *listIn) (*listOut, error) {
		q := New(s.Pool)
		reg, err := q.RegistryID(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		var kind *string
		if in.Kind != "" {
			kind = &in.Kind
		}
		rows, err := q.ListRecords(ctx, ListRecordsParams{Visible: []int64{reg}, Kind: kind})
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOut{}
		out.Body.Records = []RecordSummary{}
		for _, r := range rows {
			sm := RecordSummary{Key: r.Key, Kind: r.Kind, Name: r.Title, UpdatedAt: r.SourceUpdatedAt}
			if rec, err := recordOf(r.Metadata); err == nil && rec.Term != nil {
				sm.Status = rec.Term.Status
			}
			out.Body.Records = append(out.Body.Records, sm)
		}
		return out, nil
	})

	type saveIn struct {
		Body RegistryRecord
	}
	type saveOut struct{ Body SavedRecord }
	huma.Register(a, huma.Operation{
		OperationID: "createRecord", Method: http.MethodPost, Path: "/api/v1/registry/records", Tags: tags,
		Summary: "Create a registry record", Description: "A member creates draft terms. Referenced entities must be visible to you.",
		Middlewares: members, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *saveIn) (*saveOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		out, err := s.Save(ctx, "", in.Body, editor(ctx, vis))
		if err != nil {
			return nil, saveErr(err)
		}
		return &saveOut{Body: *out}, nil
	})

	type updateIn struct {
		Key  string `path:"key" doc:"The record key."`
		Body RegistryRecord
	}
	huma.Register(a, huma.Operation{
		OperationID: "updateRecord", Method: http.MethodPut, Path: "/api/v1/registry/records/{key}", Tags: tags,
		Summary:     "Save a new version of a registry record",
		Description: "A member edit of an approved term definition becomes a proposal and gives its ID instead of a version.",
		Middlewares: members,
	}, func(ctx context.Context, in *updateIn) (*saveOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		out, err := s.Save(ctx, unescape(in.Key), in.Body, editor(ctx, vis))
		if err != nil {
			return nil, saveErr(err)
		}
		return &saveOut{Body: *out}, nil
	})

	type viewOut struct{ Body RecordView }
	huma.Register(a, huma.Operation{
		OperationID: "getRecord", Method: http.MethodGet, Path: "/api/v1/registry/records/{key}", Tags: tags,
		Summary: "Get a registry record with its versions", Middlewares: members,
	}, func(ctx context.Context, in *keyIn) (*viewOut, error) {
		v, err := s.Get(ctx, in.key())
		if err != nil {
			return nil, saveErr(err)
		}
		return &viewOut{Body: *v}, nil
	})

	type versionIn struct {
		Key string `path:"key" doc:"The record key."`
		ID  int64  `path:"id"`
	}
	type versionOut struct {
		Body struct {
			RegistryRecord
			Diff []DiffLine `json:"diff" doc:"The change from the version before."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "getRecordVersion", Method: http.MethodGet, Path: "/api/v1/registry/records/{key}/versions/{id}", Tags: tags,
		Summary: "Get a version of a registry record with its diff", Middlewares: members,
	}, func(ctx context.Context, in *versionIn) (*versionOut, error) {
		rec, diff, err := s.VersionDiff(ctx, unescape(in.Key), in.ID)
		if err != nil {
			return nil, saveErr(err)
		}
		out := &versionOut{}
		out.Body.RegistryRecord, out.Body.Diff = *rec, diff
		return out, nil
	})

	type revertIn struct {
		Key  string `path:"key" doc:"The record key."`
		Body struct {
			VersionID int64 `json:"versionId"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "revertRecord", Method: http.MethodPost, Path: "/api/v1/registry/records/{key}/revert", Tags: tags,
		Summary: "Revert a registry record", Description: "Saves the record of an old version as a new version.",
		Middlewares: members,
	}, func(ctx context.Context, in *revertIn) (*saveOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		out, err := s.Revert(ctx, unescape(in.Key), in.Body.VersionID, editor(ctx, vis))
		if err != nil {
			return nil, saveErr(err)
		}
		return &saveOut{Body: *out}, nil
	})

	type exportOut struct {
		ContentType        string `header:"Content-Type"`
		ContentDisposition string `header:"Content-Disposition"`
		Body               []byte
	}
	huma.Register(a, huma.Operation{
		OperationID: "exportTerms", Method: http.MethodGet, Path: "/api/v1/registry/terms.csv", Tags: tags,
		Summary:     "Export the registry terms as CSV",
		Description: "Columns: key, name, synonyms, definition, domain, owner, preferred, related, status. Lists use a semicolon.",
		Middlewares: members,
		Responses:   map[string]*huma.Response{"200": {Description: "CSV", Content: map[string]*huma.MediaType{"text/csv": {Schema: &huma.Schema{Type: "string"}}}}},
	}, func(ctx context.Context, _ *struct{}) (*exportOut, error) {
		b, err := s.ExportTerms(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		return &exportOut{ContentType: "text/csv; charset=utf-8", ContentDisposition: `attachment; filename="glossary.csv"`, Body: b}, nil
	})

	type importIn struct {
		RawBody []byte `contentType:"text/csv" maxLength:"5000000"`
	}
	type importOut struct{ Body ImportResult }
	huma.Register(a, huma.Operation{
		OperationID: "importTerms", Method: http.MethodPost, Path: "/api/v1/registry/terms.csv", Tags: tags,
		Summary:     "Import registry terms from CSV",
		Description: "The export format. A row with a key or a known name saves a new version; other rows create terms. The status rules of your role apply.",
		Middlewares: members,
	}, func(ctx context.Context, in *importIn) (*importOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		res, err := s.ImportTerms(ctx, in.RawBody, editor(ctx, vis))
		if err != nil {
			return nil, saveErr(err)
		}
		return &importOut{Body: *res}, nil
	})
}
