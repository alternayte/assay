package fixtures

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// loadCatalog reads the Acme Backstage catalog.
func (s *Sources) loadCatalog() {
	b, err := corpus.ReadFile("corpus/backstage/catalog.yaml")
	if err != nil {
		panic(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var e map[string]any
		err := dec.Decode(&e)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			panic(err)
		}
		s.putEntity(e)
	}
}

func entityRef(e map[string]any) string {
	md, _ := e["metadata"].(map[string]any)
	ns, _ := md["namespace"].(string)
	if ns == "" {
		ns = "default"
	}
	name, _ := md["name"].(string)
	kind, _ := e["kind"].(string)
	return strings.ToLower(kind) + ":" + strings.ToLower(ns) + "/" + strings.ToLower(name)
}

// putEntity stores an entity with a uid and an etag from its content. The
// lock is held.
func (s *Sources) putEntity(e map[string]any) {
	md, _ := e["metadata"].(map[string]any)
	if md == nil {
		md = map[string]any{}
		e["metadata"] = md
	}
	if _, ok := md["namespace"]; !ok {
		md["namespace"] = "default"
	}
	delete(md, "etag")
	ref := entityRef(e)
	body, _ := json.Marshal(e)
	sum := sha256.Sum256(body)
	md["etag"] = hex.EncodeToString(sum[:8])
	if _, ok := md["uid"]; !ok {
		id := sha256.Sum256([]byte(ref))
		md["uid"] = hex.EncodeToString(id[:16])
	}
	e["relations"] = []any{}
	if !slices.Contains(s.catalogOrder, ref) {
		s.catalogOrder = append(s.catalogOrder, ref)
	}
	s.catalog[ref] = e
}

// PutEntity adds or changes a catalog entity. A change gets a new etag.
func (s *Sources) PutEntity(yamlDoc string) {
	var e map[string]any
	if err := yaml.Unmarshal([]byte(yamlDoc), &e); err != nil {
		panic(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putEntity(e)
}

// DeleteEntity removes a catalog entity by its ref, such as
// component:default/ledger-service.
func (s *Sources) DeleteEntity(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.catalog, ref)
	s.catalogOrder = slices.DeleteFunc(s.catalogOrder, func(r string) bool { return r == ref })
}

// CatalogKinds counts the catalog entities by kind.
func (s *Sources) CatalogKinds() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, e := range s.catalog {
		k, _ := e["kind"].(string)
		out[k]++
	}
	return out
}

// catalogCursor is the state of a paged catalog query.
type catalogCursor struct {
	Filters []string `json:"filters"`
	Offset  int      `json:"offset"`
}

// matches reports whether an entity matches one filter of the form
// key=value,key=value. Keys are kind and metadata.namespace.
func matches(e map[string]any, filter string) bool {
	md, _ := e["metadata"].(map[string]any)
	for _, cond := range strings.Split(filter, ",") {
		k, v, _ := strings.Cut(cond, "=")
		var got string
		switch k {
		case "kind":
			got, _ = e["kind"].(string)
		case "metadata.namespace":
			got, _ = md["namespace"].(string)
		case "metadata.name":
			got, _ = md["name"].(string)
		default:
			return false
		}
		if !strings.EqualFold(got, v) {
			return false
		}
	}
	return true
}

// project keeps only the fields of a comma list, such as
// kind,metadata.name.
func project(e map[string]any, fields string) map[string]any {
	if fields == "" {
		return e
	}
	out := map[string]any{}
	for _, f := range strings.Split(fields, ",") {
		top, sub, nested := strings.Cut(f, ".")
		if !nested {
			out[top] = e[top]
			continue
		}
		src, _ := e[top].(map[string]any)
		dst, _ := out[top].(map[string]any)
		if dst == nil {
			dst = map[string]any{}
			out[top] = dst
		}
		dst[sub] = src[sub]
	}
	return out
}

// backstage serves the catalog API of a Backstage backend under /backstage.
func (s *Sources) backstage(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/backstage")
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	switch {
	case p == "/api/catalog/entities/by-query":
		cur := catalogCursor{Filters: q["filter"]}
		if c := q.Get("cursor"); c != "" {
			raw, err := base64.RawURLEncoding.DecodeString(c)
			if err != nil || json.Unmarshal(raw, &cur) != nil {
				badQuery(w, "invalid cursor")
				return
			}
		}
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit <= 0 {
			limit = 20
		}
		var all []map[string]any
		for _, ref := range s.catalogOrder {
			e := s.catalog[ref]
			if len(cur.Filters) == 0 || slices.ContainsFunc(cur.Filters, func(f string) bool { return matches(e, f) }) {
				all = append(all, e)
			}
		}
		items := []map[string]any{}
		for _, e := range window(all, cur.Offset, limit) {
			items = append(items, project(e, q.Get("fields")))
		}
		info := map[string]any{}
		if next := cur.Offset + len(items); next < len(all) {
			raw, _ := json.Marshal(catalogCursor{Filters: cur.Filters, Offset: next})
			info["nextCursor"] = base64.RawURLEncoding.EncodeToString(raw)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "totalItems": len(all), "pageInfo": info})
	case strings.HasPrefix(p, "/api/catalog/entities/by-name/"):
		parts := strings.Split(strings.TrimPrefix(p, "/api/catalog/entities/by-name/"), "/")
		if len(parts) != 3 {
			http.NotFound(w, r)
			return
		}
		for i := range parts {
			parts[i], _ = url.PathUnescape(parts[i])
		}
		e, ok := s.catalog[strings.ToLower(parts[0])+":"+strings.ToLower(parts[1])+"/"+strings.ToLower(parts[2])]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"name": "NotFoundError"}})
			return
		}
		writeJSON(w, http.StatusOK, e)
	case p == "/api/catalog/entity-facets" && q.Get("facet") == "metadata.namespace":
		count := map[string]int{}
		var order []string
		for _, ref := range s.catalogOrder {
			md, _ := s.catalog[ref]["metadata"].(map[string]any)
			ns, _ := md["namespace"].(string)
			if count[ns] == 0 {
				order = append(order, ns)
			}
			count[ns]++
		}
		vals := []map[string]any{}
		for _, ns := range order {
			vals = append(vals, map[string]any{"value": ns, "count": count[ns]})
		}
		writeJSON(w, http.StatusOK, map[string]any{"facets": map[string]any{"metadata.namespace": vals}})
	default:
		http.NotFound(w, r)
	}
}
