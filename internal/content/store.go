package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/entities"
)

// errBadEntity marks an entity with an unknown type or a key that is not
// provider-qualified. Ingest drops it with a log line.
var errBadEntity = errors.New("content: the entity is not valid")

// Outcome is the result of storing one item.
type Outcome int

// The outcomes of Store.
const (
	Unchanged Outcome = iota
	Created
	Updated
)

// Hash is the content hash of an item. A version is written only when it
// changes.
func (it *Item) Hash() []byte {
	h := sha256.New()
	for _, s := range []string{it.Kind, it.Title, it.URL, it.Author, it.EditedBy, it.Body} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	vs := []any{it.Metadata, it.Entity, it.Facts}
	if len(it.Entities) > 0 {
		vs = append(vs, it.Entities)
	}
	for _, v := range vs {
		b, _ := json.Marshal(v)
		h.Write(b)
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Store writes an item of a connection in tx. When the content hash is
// unchanged it writes nothing. Otherwise it writes a version, replaces the
// passages and chunks, updates the evidence state and ingests the facts.
func Store(ctx context.Context, tx pgx.Tx, connectionID int64, domainID *int64, it *Item, log *slog.Logger) (Outcome, error) {
	q := New(tx)
	hash := it.Hash()
	existing, err := q.GetItemByExternalID(ctx, GetItemByExternalIDParams{ConnectionID: connectionID, ExternalID: it.ExternalID})
	outcome := Updated
	var itemID int64
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		outcome = Created
		itemID, err = q.InsertItem(ctx, InsertItemParams{ConnectionID: connectionID, ExternalID: it.ExternalID, Kind: it.Kind,
			Title: it.Title, Url: it.URL, Author: it.Author, SourceCreatedAt: timePtr(it.Created), SourceUpdatedAt: timePtr(it.Updated),
			ContentHash: hash, Revision: it.Revision})
		if err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	case bytes.Equal(existing.ContentHash, hash):
		if existing.Revision != it.Revision {
			if err := q.SetRevision(ctx, SetRevisionParams{ID: existing.ID, Revision: it.Revision}); err != nil {
				return 0, err
			}
		}
		return Unchanged, nil
	default:
		itemID = existing.ID
	}

	meta, _ := json.Marshal(orEmpty(it.Metadata))
	versionID, err := q.InsertVersion(ctx, InsertVersionParams{ItemID: itemID, ConnectionID: connectionID, ContentHash: hash,
		Title: it.Title, Body: it.Body, Metadata: meta, EditedBy: it.EditedBy, SourceUpdatedAt: timePtr(it.Updated)})
	if err != nil {
		return 0, err
	}
	if err := q.UpdateItem(ctx, UpdateItemParams{ID: itemID, Kind: it.Kind, Title: it.Title, Url: it.URL, Author: it.Author,
		SourceCreatedAt: timePtr(it.Created), SourceUpdatedAt: timePtr(it.Updated), ContentHash: hash, VersionID: &versionID,
		Revision: it.Revision}); err != nil {
		return 0, err
	}
	if err := q.DeleteItemPassages(ctx, itemID); err != nil {
		return 0, err
	}
	if err := q.DeleteItemChunks(ctx, itemID); err != nil {
		return 0, err
	}
	ps := Split(it.Body)
	rows := make([]InsertPassagesParams, len(ps))
	for i, p := range ps {
		rows[i] = InsertPassagesParams{ItemID: itemID, VersionID: versionID, ConnectionID: connectionID, Hash: p.Hash,
			Ordinal: int32(p.Ordinal), Kind: p.Kind, HeadingPath: orEmptyStrings(p.HeadingPath), Text: p.Text, Norm: p.Norm} //nolint:gosec // Ordinals are small.
	}
	if len(rows) > 0 {
		if _, err := q.InsertPassages(ctx, rows); err != nil {
			return 0, err
		}
	}
	for _, c := range Chunks(it.Title, ps) {
		body := c.Text[len(prefix(it.Title, c.HeadingPath)):]
		if err := q.InsertChunk(ctx, InsertChunkParams{ItemID: itemID, VersionID: versionID, ConnectionID: connectionID,
			Ordinal: int32(c.Ordinal), HeadingPath: orEmptyStrings(c.HeadingPath), Text: c.Text, PassageHashes: c.PassageHashes, //nolint:gosec // Ordinals are small.
			Tokens: int32(c.Tokens), Title: it.Title, Heading: strings.Join(c.HeadingPath, " "), Body: body}); err != nil { //nolint:gosec // Chunks have at most 400 tokens.
			return 0, err
		}
	}
	if err := q.RefreshEvidence(ctx, itemID); err != nil {
		return 0, err
	}
	in := ingest{q: q, tx: tx, connectionID: connectionID, domainID: domainID, itemID: itemID, log: log, ids: map[string]int64{}}
	var entityID *int64
	if it.Entity != nil {
		id, err := in.entity(ctx, *it.Entity)
		switch {
		case errors.Is(err, errBadEntity):
			log.Warn("entity dropped", "reason", err.Error())
		case err != nil:
			return 0, err
		default:
			entityID = &id
		}
	}
	if err := q.SetItemEntity(ctx, SetItemEntityParams{ID: itemID, EntityID: entityID}); err != nil {
		return 0, err
	}
	for _, e := range it.Entities {
		if _, err := in.entity(ctx, e); errors.Is(err, errBadEntity) {
			log.Warn("entity dropped", "reason", err.Error())
		} else if err != nil {
			return 0, err
		}
	}
	for _, f := range it.Facts {
		if err := in.fact(ctx, f, ps, it.Updated); err != nil {
			return 0, err
		}
	}
	return outcome, nil
}

type ingest struct {
	q            *Queries
	tx           pgx.Tx
	connectionID int64
	domainID     *int64
	itemID       int64
	log          *slog.Logger
	ids          map[string]int64
	kind         string // the connection kind, read on first use
}

// entity upserts an entity with its source and aliases.
func (in *ingest) entity(ctx context.Context, e EntityRef) (int64, error) {
	if id, ok := in.ids[e.Key]; ok {
		return id, nil
	}
	if err := entities.CheckRef(e.Type, e.Key); err != nil {
		return 0, fmt.Errorf("%w: %w", errBadEntity, err)
	}
	id, err := in.q.UpsertEntity(ctx, UpsertEntityParams{Type: e.Type, Key: e.Key, DomainID: in.domainID})
	if err != nil {
		return 0, err
	}
	if err := in.q.AddEntitySource(ctx, AddEntitySourceParams{EntityID: id, ConnectionID: in.connectionID}); err != nil {
		return 0, err
	}
	if n := entities.NormalizeAlias(e.Name); n != "" {
		if err := in.q.AddAlias(ctx, AddAliasParams{EntityID: id, ConnectionID: in.connectionID, Alias: strings.TrimSpace(e.Name), AliasNorm: n, IsName: true}); err != nil {
			return 0, err
		}
		if err := in.q.ClearOtherNames(ctx, ClearOtherNamesParams{EntityID: id, ConnectionID: in.connectionID, AliasNorm: n}); err != nil {
			return 0, err
		}
	}
	for _, a := range e.Aliases {
		if n := entities.NormalizeAlias(a); n != "" {
			if err := in.q.AddAlias(ctx, AddAliasParams{EntityID: id, ConnectionID: in.connectionID, Alias: strings.TrimSpace(a), AliasNorm: n}); err != nil {
				return 0, err
			}
		}
	}
	in.ids[e.Key] = id
	return id, nil
}

// fact ingests one deterministic claim with field evidence. A fact that
// breaks the predicate registry, or whose quote is in no passage, is
// dropped with a log line.
func (in *ingest) fact(ctx context.Context, f Fact, ps []Passage, at time.Time) error {
	objType := claims.ObjText
	if f.Object != nil {
		objType = f.Object.Type
	} else if p, ok := claims.Registry[f.Predicate]; ok && len(p.Objects) == 1 && p.Objects[0] == claims.ObjDate {
		if _, err := time.Parse("2006-01-02", f.Text); err != nil {
			in.log.Warn("fact dropped", "predicate", f.Predicate, "reason", "not a date")
			return nil //nolint:nilerr // A bad date drops the fact, not the item.
		}
		objType = claims.ObjDate
	}
	if err := claims.Check(f.Predicate, f.Subject.Type, objType); err != nil {
		in.log.Warn("fact dropped", "predicate", f.Predicate, "reason", err.Error())
		return nil
	}
	quote := Normalize(f.Quote)
	var passageHash []byte
	for _, p := range ps {
		if strings.Contains(p.Norm, quote) {
			passageHash = p.Hash
			break
		}
	}
	if passageHash == nil {
		in.log.Warn("fact dropped", "predicate", f.Predicate, "reason", "the quote is in no passage")
		return nil
	}
	subject, err := in.entity(ctx, f.Subject)
	if errors.Is(err, errBadEntity) {
		in.log.Warn("fact dropped", "predicate", f.Predicate, "reason", err.Error())
		return nil
	}
	if err != nil {
		return err
	}
	var objectID *int64
	var objectText *string
	objectNorm := ""
	if f.Object != nil {
		id, err := in.entity(ctx, *f.Object)
		if errors.Is(err, errBadEntity) {
			in.log.Warn("fact dropped", "predicate", f.Predicate, "reason", err.Error())
			return nil
		}
		if err != nil {
			return err
		}
		objectID = &id
	} else {
		text := strings.TrimSpace(f.Text)
		objectText = &text
		objectNorm = entities.NormalizeAlias(text)
	}
	claimID, err := in.claim(ctx, subject, f.Predicate, objectID, objectText, objectNorm, at)
	if err != nil {
		return err
	}
	source := f.Source
	if source == "" {
		if source, err = in.sourceType(ctx); err != nil {
			return err
		}
	}
	return in.evidence(ctx, claimID, passageHash, "field", "supports", quote, source, at)
}

// evidence adds an evidence row. New support for a claim reopens the
// resolutions that chose another claim.
func (in *ingest) evidence(ctx context.Context, claimID int64, passageHash []byte, kind, stance, quote, source string, now time.Time) error {
	inserted, err := in.q.AddEvidence(ctx, AddEvidenceParams{ClaimID: claimID, ConnectionID: in.connectionID, ItemID: in.itemID,
		PassageHash: passageHash, Kind: kind, Stance: stance, Quote: quote, SourceType: source})
	if err != nil || !inserted || stance != "supports" {
		return err
	}
	conns, err := in.q.ClaimGroupConnections(ctx, claimID)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	return claims.Reopen(ctx, in.tx, claimID, conns, now)
}

// sourceType is the evidence source type of the connection: its kind, or
// repository for GitHub and Azure DevOps.
func (in *ingest) sourceType(ctx context.Context) (string, error) {
	if in.kind == "" {
		k, err := in.q.ConnectionKind(ctx, in.connectionID)
		if err != nil {
			return "", err
		}
		in.kind = k
	}
	switch in.kind {
	case "github", "azdo":
		return "repository", nil
	}
	return in.kind, nil
}

// claim finds or creates a claim. A time-scoped predicate keeps the newest
// value when it is unchanged, and otherwise starts a new dated value.
func (in *ingest) claim(ctx context.Context, subject int64, predicate string, objectID *int64, objectText *string, objectNorm string, at time.Time) (int64, error) {
	var validFrom *time.Time
	if claims.Registry[predicate].TimeScoped {
		latest, err := in.q.LatestClaim(ctx, LatestClaimParams{SubjectID: subject, Predicate: predicate})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		if err == nil && latest.ObjectNorm == objectNorm && latest.ObjectEntityID == deref(objectID) {
			if latest.RetractedAt != nil {
				if err := in.q.Unretract(ctx, latest.ID); err != nil {
					return 0, err
				}
			}
			return latest.ID, nil
		}
		if at.IsZero() {
			at = time.Now()
		}
		t := at.UTC()
		validFrom = &t
	}
	id, err := in.q.UpsertClaim(ctx, UpsertClaimParams{SubjectID: subject, Predicate: predicate, ObjectEntityID: objectID,
		ObjectText: objectText, ObjectNorm: objectNorm, ValidFrom: validFrom})
	if err != nil {
		return 0, fmt.Errorf("content: claim %s: %w", predicate, err)
	}
	return id, nil
}

// Tombstone deletes an item in tx: its versions, passages, chunks and
// evidence. Claims left without evidence are retracted.
func Tombstone(ctx context.Context, tx pgx.Tx, itemID int64, now time.Time) error {
	q := New(tx)
	ids, err := q.ItemClaims(ctx, itemID)
	if err != nil {
		return err
	}
	if _, err := q.DeleteItem(ctx, itemID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = q.RetractUnsupported(ctx, RetractUnsupportedParams{Now: &now, Ids: ids})
	return err
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
