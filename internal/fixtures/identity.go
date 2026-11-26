package fixtures

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The fake identity provider is an Entra ID tenant of Acme: an OpenID
// Connect issuer under /entra/<tenant>/v2.0, its client-credentials token
// endpoint, and a Microsoft Graph API under /graph/v1.0 with Acme users,
// groups and Teams channels.
const (
	// IdentityTenant is the tenant of the fake issuer.
	IdentityTenant = "acme-tenant"
	// IdentityClientID and IdentityClientSecret are the app registration.
	IdentityClientID     = "assay-acme-app"
	IdentityClientSecret = "acme-app-secret"
)

// The Acme directory groups.
const (
	GroupStaff    = "6f1c0000-0000-4000-a000-000000000001"
	GroupPayments = "6f1c0000-0000-4000-a000-000000000002"
	GroupPlatform = "6f1c0000-0000-4000-a000-000000000003"
	GroupEng      = "6f1c0000-0000-4000-a000-000000000004"
)

// The Acme directory users.
const (
	UserAda   = "a11c0000-0000-4000-b000-000000000001"
	UserGrace = "a11c0000-0000-4000-b000-000000000002"
	UserLinus = "a11c0000-0000-4000-b000-000000000003"
	UserKim   = "a11c0000-0000-4000-b000-000000000004"
)

// DirectoryUser is one user of the fake directory.
type DirectoryUser struct {
	ID      string
	Mail    string
	Name    string
	Enabled bool
	Deleted bool
	// version is the change counter of the last change.
	version int
}

type directoryGroup struct {
	id      string
	name    string
	users   []string
	groups  []string
	version int
}

type authCode struct {
	user, clientID, redirectURI, nonce, challenge string
	expires                                       time.Time
}

// Identity is the fake Entra ID tenant. It is safe for concurrent use.
type Identity struct {
	mu      sync.Mutex
	key     *rsa.PrivateKey
	users   map[string]*DirectoryUser
	groups  map[string]*directoryGroup
	codes   map[string]authCode
	tokens  map[string]time.Time
	version int
	// chat holds the Teams teams, channels and messages.
	chat *teamsState
	hits []Hit
	// PageSize is the size of a Graph page.
	PageSize int
	// Now is the clock.
	Now func() time.Time
}

// NewIdentity returns the fake tenant with the Acme users and groups. Ada is
// in Staff and Payments, Grace in Staff and Platform, Linus in Staff and
// Payments, and Kim, a contractor, in no group. Engineering holds the
// Payments and Platform groups.
func NewIdentity() *Identity {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	id := &Identity{key: key, users: map[string]*DirectoryUser{}, groups: map[string]*directoryGroup{},
		codes: map[string]authCode{}, tokens: map[string]time.Time{}, PageSize: 2, Now: time.Now, chat: loadTeams(time.Now())}
	for _, u := range []DirectoryUser{
		{ID: UserAda, Mail: "ada@acme.test", Name: "Ada Park"},
		{ID: UserGrace, Mail: "grace@acme.test", Name: "Grace Obi"},
		{ID: UserLinus, Mail: "linus@acme.test", Name: "Linus Vale"},
		{ID: UserKim, Mail: "kim@acme.test", Name: "Kim Ross"},
	} {
		u.Enabled = true
		id.users[u.ID] = &u
	}
	id.groups[GroupStaff] = &directoryGroup{id: GroupStaff, name: "Acme Staff", users: []string{UserAda, UserGrace, UserLinus}}
	id.groups[GroupPayments] = &directoryGroup{id: GroupPayments, name: "Payments Team", users: []string{UserAda, UserLinus}}
	id.groups[GroupPlatform] = &directoryGroup{id: GroupPlatform, name: "Platform Team", users: []string{UserGrace}}
	id.groups[GroupEng] = &directoryGroup{id: GroupEng, name: "Engineering", groups: []string{GroupPayments, GroupPlatform}}
	return id
}

// Issuer returns the issuer URL of the tenant on a fixture server.
func Issuer(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/entra/" + IdentityTenant + "/v2.0"
}

// GraphBase returns the Graph URL of a fixture server.
func GraphBase(baseURL string) string { return strings.TrimRight(baseURL, "/") + "/graph/v1.0" }

// SetEnabled sets accountEnabled of a user.
func (d *Identity) SetEnabled(userID string, enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if u, ok := d.users[userID]; ok {
		d.version++
		u.Enabled, u.version = enabled, d.version
	}
}

// Delete deletes a user.
func (d *Identity) Delete(userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if u, ok := d.users[userID]; ok {
		d.version++
		u.Deleted, u.version = true, d.version
	}
}

// SetMember adds a user to a group or removes the user from it.
func (d *Identity) SetMember(groupID, userID string, member bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	g, ok := d.groups[groupID]
	if !ok {
		return
	}
	g.users = slices.DeleteFunc(g.users, func(u string) bool { return u == userID })
	if member {
		g.users = append(g.users, userID)
	}
	d.version++
	g.version = d.version
}

// members returns the users of a group, also through nested groups. The
// lock is held.
func (d *Identity) members(groupID string, seen map[string]bool) []string {
	g, ok := d.groups[groupID]
	if !ok || seen[groupID] {
		return nil
	}
	seen[groupID] = true
	out := append([]string(nil), g.users...)
	for _, sub := range g.groups {
		out = append(out, d.members(sub, seen)...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// groupsOf returns the groups of a user, also through nested groups, as the
// groups claim carries them. The lock is held.
func (d *Identity) groupsOf(userID string) []string {
	var out []string
	for id := range d.groups {
		if slices.Contains(d.members(id, map[string]bool{}), userID) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Hits returns the requests that the tenant received.
func (d *Identity) Hits() []Hit {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.hits)
}

func (d *Identity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.hits = append(d.hits, Hit{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, At: time.Now()})
	d.mu.Unlock()
	tenant := "/entra/" + IdentityTenant
	switch p := r.URL.Path; {
	case r.Method == http.MethodGet && p == tenant+"/v2.0/.well-known/openid-configuration":
		iss := origin(r) + tenant + "/v2.0"
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                 iss,
			"authorization_endpoint": origin(r) + tenant + "/oauth2/v2.0/authorize",
			"token_endpoint":         origin(r) + tenant + "/oauth2/v2.0/token",
			"jwks_uri":               origin(r) + tenant + "/discovery/v2.0/keys",
		})
	case r.Method == http.MethodGet && p == tenant+"/discovery/v2.0/keys":
		pub := d.key.PublicKey
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "acme-1", "alg": "RS256", "use": "sig",
			"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	case r.Method == http.MethodGet && p == tenant+"/oauth2/v2.0/authorize":
		d.authorize(w, r)
	case r.Method == http.MethodPost && p == tenant+"/oauth2/v2.0/token":
		d.token(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/graph/v1.0/"):
		d.graph(w, r, strings.TrimPrefix(p, "/graph/v1.0"))
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/control/directory/"):
		d.control(w, r, strings.TrimPrefix(p, "/control/directory"))
	default:
		http.NotFound(w, r)
	}
}

// authorize signs in the user of login_hint at once. Without login_hint it
// shows a page with a link for each enabled user.
func (d *Identity) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != IdentityClientID || q.Get("response_type") != "code" || q.Get("redirect_uri") == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	hint := q.Get("login_hint")
	if hint == "" {
		var b strings.Builder
		b.WriteString("<!doctype html><title>Acme sign-in</title><h1>Acme sign-in</h1><ul>")
		ids := make([]string, 0, len(d.users))
		for id := range d.users {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return d.users[ids[i]].Mail < d.users[ids[j]].Mail })
		for _, id := range ids {
			u := d.users[id]
			if !u.Enabled || u.Deleted {
				continue
			}
			q2 := r.URL.Query()
			q2.Set("login_hint", u.Mail)
			fmt.Fprintf(&b, `<li><a href="%s">%s (%s)</a></li>`, html.EscapeString(r.URL.Path+"?"+q2.Encode()),
				html.EscapeString(u.Name), html.EscapeString(u.Mail))
		}
		b.WriteString("</ul>")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
		return
	}
	var user *DirectoryUser
	for _, u := range d.users {
		if strings.EqualFold(u.Mail, hint) {
			user = u
		}
	}
	target, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	back := target.Query()
	back.Set("state", q.Get("state"))
	if user == nil || !user.Enabled || user.Deleted {
		back.Set("error", "access_denied")
	} else {
		code := randomToken()
		d.codes[code] = authCode{user: user.ID, clientID: IdentityClientID, redirectURI: q.Get("redirect_uri"),
			nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), expires: d.Now().Add(5 * time.Minute)}
		back.Set("code", code)
	}
	target.RawQuery = back.Encode()
	// An authorization server returns to the redirect URI of the request.
	http.Redirect(w, r, target.String(), http.StatusFound) //nolint:gosec // The fake IdP serves only tests.
}

func (d *Identity) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f := r.PostForm
	if f.Get("client_id") != IdentityClientID || f.Get("client_secret") != IdentityClientSecret {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch f.Get("grant_type") {
	case "client_credentials":
		if !strings.HasSuffix(f.Get("scope"), "/.default") {
			tokenError(w, http.StatusBadRequest, "invalid_scope")
			return
		}
		tok := randomToken()
		d.tokens[tok] = d.Now().Add(time.Hour)
		writeJSON(w, http.StatusOK, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600})
	case "authorization_code":
		c, ok := d.codes[f.Get("code")]
		delete(d.codes, f.Get("code"))
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if !ok || d.Now().After(c.expires) || c.redirectURI != f.Get("redirect_uri") ||
			(c.challenge != "" && b64(sum[:]) != c.challenge) {
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		u := d.users[c.user]
		now := d.Now()
		idToken, err := d.sign(map[string]any{
			"iss": origin(r) + "/entra/" + IdentityTenant + "/v2.0", "aud": IdentityClientID,
			// Entra ID gives a pairwise subject per app. The object ID is oid.
			"sub": "pairwise-" + u.ID[len(u.ID)-6:], "oid": u.ID, "tid": IdentityTenant,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": c.nonce,
			"email": u.Mail, "name": u.Name, "preferred_username": u.Mail, "groups": d.groupsOf(u.ID),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": randomToken(), "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (d *Identity) sign(claims map[string]any) (string, error) {
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "acme-1", "typ": "JWT"})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64(head) + "." + b64(body)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, d.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return input + "." + b64(sig), nil
}

// graph serves the Graph reads.
func (d *Identity) graph(w http.ResponseWriter, r *http.Request, path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if exp, ok := d.tokens[tok]; !ok || d.Now().After(exp) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "InvalidAuthenticationToken"}})
		return
	}
	q := r.URL.Query()
	base := origin(r) + "/graph/v1.0"
	if d.teams(w, r, base, path) {
		return
	}
	switch {
	case path == "/users/delta":
		d.delta(w, q, base)
	case path == "/groups":
		var out []map[string]string
		prefix := ""
		if f := q.Get("$filter"); f != "" {
			p, ok := strings.CutPrefix(f, "startswith(displayName,'")
			if !ok || !strings.HasSuffix(p, "')") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			prefix = strings.ToLower(strings.ReplaceAll(strings.TrimSuffix(p, "')"), "''", "'"))
		}
		for _, g := range d.groups {
			if strings.HasPrefix(strings.ToLower(g.name), prefix) {
				out = append(out, map[string]string{"id": g.id, "displayName": g.name})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["displayName"] < out[j]["displayName"] })
		writeJSON(w, http.StatusOK, map[string]any{"value": orEmptyList(out)})
	case strings.HasPrefix(path, "/groups/") && strings.HasSuffix(path, "/transitiveMembers"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/groups/"), "/transitiveMembers")
		g, ok := d.groups[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "Request_ResourceNotFound"}})
			return
		}
		var objs []map[string]string
		for _, sub := range g.groups {
			objs = append(objs, map[string]string{"@odata.type": "#microsoft.graph.group", "id": sub})
		}
		for _, u := range d.members(id, map[string]bool{}) {
			if !d.users[u].Deleted {
				objs = append(objs, map[string]string{"@odata.type": "#microsoft.graph.user", "id": u})
			}
		}
		d.page(w, q, base+path, objs, "")
	default:
		http.NotFound(w, r)
	}
}

// delta serves /users/delta. A delta token is the change counter of the
// read. Without one, the read lists every user that is not deleted.
func (d *Identity) delta(w http.ResponseWriter, q url.Values, base string) {
	since := -1
	if t := q.Get("$deltatoken"); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n > d.version {
			writeJSON(w, http.StatusGone, map[string]any{"error": map[string]string{"code": "SyncStateNotFound"}})
			return
		}
		since = n
	} else if s := q.Get("$skiptoken"); s != "" {
		since, _ = strconv.Atoi(strings.SplitN(s, ".", 2)[0])
	}
	ids := make([]string, 0, len(d.users))
	for id := range d.users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var objs []map[string]any
	for _, id := range ids {
		u := d.users[id]
		switch {
		case since < 0 && u.Deleted:
		case since < 0:
			objs = append(objs, map[string]any{"id": u.ID, "mail": u.Mail, "accountEnabled": u.Enabled})
		case u.version <= since:
		case u.Deleted:
			objs = append(objs, map[string]any{"id": u.ID, "@removed": map[string]string{"reason": "changed"}})
		default:
			// A delta page names only the fields that changed.
			objs = append(objs, map[string]any{"id": u.ID, "accountEnabled": u.Enabled})
		}
	}
	d.page(w, q, base+"/users/delta", objs, strconv.Itoa(since)+"."+strconv.Itoa(d.version))
}

// page writes one page of objs. A $skiptoken of the form <state>.<offset>
// selects the page. delta is the state of a delta read, or empty.
func (d *Identity) page(w http.ResponseWriter, q url.Values, link string, objs any, delta string) {
	list := toAnyList(objs)
	offset := 0
	if s := q.Get("$skiptoken"); s != "" {
		parts := strings.Split(s, ".")
		offset, _ = strconv.Atoi(parts[len(parts)-1])
	}
	end := min(offset+d.PageSize, len(list))
	offset = min(offset, end)
	out := map[string]any{"value": list[offset:end]}
	if end < len(list) {
		next := url.Values{}
		for k, v := range q {
			if k != "$skiptoken" && k != "$deltatoken" {
				next[k] = v
			}
		}
		state := ""
		if delta != "" {
			state = strings.SplitN(delta, ".", 2)[0] + "."
		}
		next.Set("$skiptoken", state+strconv.Itoa(end))
		out["@odata.nextLink"] = link + "?" + next.Encode()
	} else if delta != "" {
		out["@odata.deltaLink"] = link + "?$deltatoken=" + strings.SplitN(delta, ".", 2)[1]
	}
	writeJSON(w, http.StatusOK, out)
}

// control changes the directory for browser tests: POST
// /control/directory/users/{id} with {"accountEnabled": bool} or
// {"deleted": true}, and POST /control/directory/groups/{id}/members/{user}
// with {"member": bool}.
func (d *Identity) control(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var in struct {
		AccountEnabled *bool `json:"accountEnabled"`
		Deleted        bool  `json:"deleted"`
		Member         bool  `json:"member"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch {
	case len(parts) == 2 && parts[0] == "users" && in.Deleted:
		d.Delete(parts[1])
	case len(parts) == 2 && parts[0] == "users" && in.AccountEnabled != nil:
		d.SetEnabled(parts[1], *in.AccountEnabled)
	case len(parts) == 4 && parts[0] == "groups" && parts[2] == "members":
		d.SetMember(parts[1], parts[3], in.Member)
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toAnyList(v any) []any {
	switch l := v.(type) {
	case []map[string]any:
		out := make([]any, len(l))
		for i := range l {
			out[i] = l[i]
		}
		return out
	case []map[string]string:
		out := make([]any, len(l))
		for i := range l {
			out[i] = l[i]
		}
		return out
	}
	return []any{}
}

func orEmptyList(l []map[string]string) []map[string]string {
	if l == nil {
		return []map[string]string{}
	}
	return l
}

func tokenError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": "AADSTS70000: the fake tenant refused the request."})
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return b64(b)
}
