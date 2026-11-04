package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// indexHash is the hash of what the passages and chunks of an item are made
// from: the title, the body and the given passages. A change to only the
// metadata or the editor keeps it.
func (it *Item) indexHash() []byte {
	h := sha256.New()
	h.Write([]byte(it.Title))
	h.Write([]byte{0})
	h.Write([]byte(it.Body))
	h.Write([]byte{0})
	if it.Passages != nil {
		b, _ := json.Marshal(it.Passages)
		h.Write(b)
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
// passages and chunks when the index hash changed, updates the evidence
// state and ingests the facts.
func Store(ctx context.Context, tx pgx.Tx, connectionID int64, domainID *int64, it *Item, log *slog.Logger) (Outcome, error) {
	q := New(tx)
	if strings.TrimSpace(it.Title) == "" {
		it.Title = fallbackTitle(it)
	}
	hash, index := it.Hash(), it.indexHash()
	existing, err := q.GetItemByExternalID(ctx, GetItemByExternalIDParams{ConnectionID: connectionID, ExternalID: it.ExternalID})
	outcome := Updated
	var itemID int64
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		outcome = Created
		itemID, err = q.InsertItem(ctx, InsertItemParams{ConnectionID: connectionID, ExternalID: it.ExternalID, Kind: it.Kind,
			Title: it.Title, Url: it.URL, Author: it.Author, SourceCreatedAt: timePtr(it.Created), SourceUpdatedAt: timePtr(it.Updated),
			ContentHash: hash, Revision: it.Revision, IndexHash: index})
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
		Revision: it.Revision, IndexHash: index}); err != nil {
		return 0, err
	}
	var ps []Passage
	if outcome == Updated && bytes.Equal(existing.IndexHash, index) {
		if err := q.SetPassagesVersion(ctx, SetPassagesVersionParams{ItemID: itemID, VersionID: versionID}); err != nil {
			return 0, err
		}
		if err := q.SetChunksVersion(ctx, SetChunksVersionParams{ItemID: itemID, VersionID: versionID}); err != nil {
			return 0, err
		}
		if ps = it.Passages; ps == nil {
			ps = Split(it.Body)
		}
	} else if ps, _, err = writeIndex(ctx, q, itemID, versionID, connectionID, it.Title, it.Body, it.Passages); err != nil {
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
	in.origin = it.Origin
	if in.origin == "" {
		in.origin = "source"
	}
	var before []int64
	if it.DropStale {
		if before, err = q.ItemClaims(ctx, itemID); err != nil {
			return 0, err
		}
	}
	for _, f := range it.Facts {
		if err := in.fact(ctx, f, ps, it.Updated); err != nil {
			return 0, err
		}
	}
	if it.DropStale {
		if err := q.DeleteStaleItemEvidence(ctx, itemID); err != nil {
			return 0, err
		}
		if len(before) > 0 {
			now := it.Updated
			if _, err := q.RetractUnsupported(ctx, RetractUnsupportedParams{Now: &now, Ids: before}); err != nil {
				return 0, err
			}
		}
	}
	return outcome, nil
}

// writeIndex replaces the passages and chunks of an item with those of a
// body and updates the evidence state. An old chunk whose text is in the
// new chunks stays, with its vectors, so that an edit embeds only the
// chunks it changed. It returns the passages and the number of chunks.
func writeIndex(ctx context.Context, q *Queries, itemID, versionID, connectionID int64, title, body string, ps []Passage) ([]Passage, int, error) {
	if err := q.DeleteItemPassages(ctx, itemID); err != nil {
		return nil, 0, err
	}
	if ps == nil {
		ps = Split(body)
	}
	rows := make([]InsertPassagesParams, len(ps))
	for i, p := range ps {
		rows[i] = InsertPassagesParams{ItemID: itemID, VersionID: versionID, ConnectionID: connectionID, Hash: p.Hash,
			Ordinal: int32(p.Ordinal), Kind: p.Kind, HeadingPath: orEmptyStrings(p.HeadingPath), Text: p.Text, Norm: p.Norm, //nolint:gosec // Ordinals are small.
			StartLine: lineOf(p.StartLine), EndLine: lineOf(p.EndLine)}
	}
	if len(rows) > 0 {
		if _, err := q.InsertPassages(ctx, rows); err != nil {
			return nil, 0, err
		}
	}
	chunks := Chunks(title, ps)
	if err := writeChunks(ctx, q, itemID, versionID, connectionID, title, chunks); err != nil {
		return nil, 0, err
	}
	if err := q.RefreshEvidence(ctx, itemID); err != nil {
		return nil, 0, err
	}
	return ps, len(chunks), nil
}

type keptChunk struct {
	ID          int64    `json:"id"`
	Ordinal     int      `json:"ordinal"`
	HeadingPath []string `json:"heading_path"`
	Hashes      [][]byte `json:"hashes"`
}

type newChunk struct {
	Ordinal     int      `json:"ordinal"`
	HeadingPath []string `json:"heading_path"`
	Text        string   `json:"text"`
	Hashes      [][]byte `json:"hashes"`
	Tokens      int      `json:"tokens"`
	Heading     string   `json:"heading"`
	Body        string   `json:"body"`
	TextHash    []byte   `json:"text_hash"`
}

// writeChunks keeps each old chunk of an item whose text hash is in the new
// chunks, deletes the other old chunks and inserts the rest in one
// statement.
func writeChunks(ctx context.Context, q *Queries, itemID, versionID, connectionID int64, title string, chunks []Chunk) error {
	old, err := q.ItemChunkHashes(ctx, itemID)
	if err != nil {
		return err
	}
	byHash := map[string][]int64{}
	for _, c := range old {
		if c.TextHash != nil {
			byHash[string(c.TextHash)] = append(byHash[string(c.TextHash)], c.ID)
		}
	}
	kept := map[int64]bool{}
	var keep []keptChunk
	var add []newChunk
	for _, c := range chunks {
		sum := sha256.Sum256([]byte(c.Text))
		if ids := byHash[string(sum[:])]; len(ids) > 0 {
			byHash[string(sum[:])] = ids[1:]
			kept[ids[0]] = true
			keep = append(keep, keptChunk{ID: ids[0], Ordinal: c.Ordinal, HeadingPath: orEmptyStrings(c.HeadingPath), Hashes: c.PassageHashes})
			continue
		}
		add = append(add, newChunk{Ordinal: c.Ordinal, HeadingPath: orEmptyStrings(c.HeadingPath), Text: c.Text, Hashes: c.PassageHashes,
			Tokens: c.Tokens, Heading: strings.Join(c.HeadingPath, " "), Body: c.Text[len(prefix(title, c.HeadingPath)):], TextHash: sum[:]})
	}
	var gone []int64
	for _, c := range old {
		if !kept[c.ID] {
			gone = append(gone, c.ID)
		}
	}
	if len(gone) > 0 {
		if err := q.DeleteChunks(ctx, gone); err != nil {
			return err
		}
	}
	if len(keep) > 0 {
		b, err := json.Marshal(keep)
		if err != nil {
			return err
		}
		if err := q.KeepChunks(ctx, KeepChunksParams{VersionID: versionID, Rows: b}); err != nil {
			return err
		}
	}
	if len(add) == 0 {
		return nil
	}
	b, err := json.Marshal(add)
	if err != nil {
		return err
	}
	return q.InsertChunks(ctx, InsertChunksParams{ItemID: itemID, VersionID: versionID, ConnectionID: connectionID, Title: title, Rows: b})
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
	origin       string
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
	claimID, err := in.claim(ctx, subject, f.Predicate, objectID, objectText, objectNorm, "", in.origin, at)
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
func (in *ingest) claim(ctx context.Context, subject int64, predicate string, objectID *int64, objectText *string, objectNorm, scope, origin string,
	at time.Time) (int64, error) {
	var validFrom *time.Time
	if claims.Registry[predicate].TimeScoped {
		latest, err := in.q.LatestClaim(ctx, LatestClaimParams{SubjectID: subject, Predicate: predicate, ScopeEnv: scope})
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
		ObjectText: objectText, ObjectNorm: objectNorm, ScopeEnv: scope, ValidFrom: validFrom, Origin: origin})
	if err != nil {
		return 0, fmt.Errorf("content: claim %s: %w", predicate, err)
	}
	return id, nil
}

// Tombstone deletes an item in tx: its versions, passages, chunks and
// evidence. Claims left without evidence are retracted. The entity of the
// item, and each entity that a claim of the item names, loses the source and
// the aliases of the connection when nothing else of the connection names it. An entity without a source is visible to no
// one; it stays, so that its retracted claims stay on record.
func Tombstone(ctx context.Context, tx pgx.Tx, itemID int64, now time.Time) error {
	q := New(tx)
	ids, err := q.ItemClaims(ctx, itemID)
	if err != nil {
		return err
	}
	ent, err := q.ItemEntity(ctx, itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := q.DeleteItem(ctx, itemID); err != nil {
		return err
	}
	ents, err := q.ClaimEntities(ctx, orEmptyIDs(ids))
	if err != nil {
		return err
	}
	if ent.EntityID != nil && !slices.Contains(ents, *ent.EntityID) {
		ents = append(ents, *ent.EntityID)
	}
	if len(ids) > 0 {
		if _, err := q.RetractUnsupported(ctx, RetractUnsupportedParams{Now: &now, Ids: ids}); err != nil {
			return err
		}
	}
	for _, e := range ents {
		if _, err := q.DropEntitySource(ctx, DropEntitySourceParams{EntityID: e, ConnectionID: ent.ConnectionID}); err != nil {
			return err
		}
	}
	return nil
}

func orEmptyIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// DeleteSourceless deletes the entities of ids that have no source left and
// no claim with evidence. Entities merged into a deleted entity become
// active again, and merges drop the deleted losers.
func DeleteSourceless(ctx context.Context, tx pgx.Tx, ids []int64) error {
	_, err := New(tx).DeleteSourcelessEntities(ctx, append([]int64{}, ids...))
	return err
}

// lineOf is the stored line of a passage: nil for a passage without lines.
func lineOf(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := int32(n) //nolint:gosec // Files have at most a few hundred thousand lines.
	return &v
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

// maxFallbackTitle is the length limit of a title made from the body.
const maxFallbackTitle = 80

// fallbackTitle is the title of an item that has none: the first heading of
// the body, else its first line, cut to maxFallbackTitle runes. An item with
// an empty body gets its kind and external ID.
func fallbackTitle(it *Item) string {
	first := ""
	for _, line := range strings.Split(it.Body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}
		if h := strings.TrimSpace(strings.TrimLeft(line, "#")); strings.HasPrefix(line, "#") && h != "" {
			first = h
			break
		}
		if first == "" {
			first = line
		}
	}
	if first == "" {
		return it.Kind + " " + it.ExternalID
	}
	if r := []rune(first); len(r) > maxFallbackTitle {
		return string(r[:maxFallbackTitle])
	}
	return first
}
