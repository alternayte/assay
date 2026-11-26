package agents

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
)

// Connect holds the URLs that an agent needs for this instance
// (REQ-AGT-007).
type Connect struct {
	MCPURL      string `json:"mcpUrl"`
	GlossaryURL string `json:"glossaryUrl"`
	OpenAPIURL  string `json:"openapiUrl"`
	DocsURL     string `json:"docsUrl"`
	OAuth       bool   `json:"oauth" doc:"True when a hosted MCP client can sign in with OAuth at the MCP URL."`
}

// ConnectedApp is one MCP client that the caller approved.
type ConnectedApp struct {
	ClientID    string    `json:"clientId"`
	Name        string    `json:"name"`
	Host        string    `json:"host" doc:"The host of the client. A client cannot fake it."`
	Kind        string    `json:"kind" enum:"static,dynamic,cimd" doc:"static is verified by Assay. dynamic and cimd registered themselves."`
	Scopes      []string  `json:"scopes"`
	ConnectedAt time.Time `json:"connectedAt" doc:"The time of the last approval."`
}

// OAuthStatus is the state of OAuth sign-in of MCP clients.
type OAuthStatus struct {
	On       bool   `json:"on"`
	LockedBy string `json:"lockedBy" doc:"The environment variable that sets the field. Empty when none does."`
}

// Register adds the route that the Connect an agent page reads, the
// Connected apps list and the OAuth status.
func (s *Service) Register(a huma.API) {
	type out struct{ Body Connect }
	huma.Register(a, huma.Operation{
		OperationID: "getConnect", Method: http.MethodGet, Path: "/api/v1/connect", Tags: []string{"agents"},
		Summary:     "Get the agent URLs of this instance",
		Description: "The MCP endpoint, the glossary Markdown, the OpenAPI document and the API reference.",
		Middlewares: huma.Middlewares{api.Require(auth.RoleMember)},
	}, func(ctx context.Context, _ *struct{}) (*out, error) {
		on, err := s.OAuthOn(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		base := strings.TrimRight(s.BaseURL, "/")
		return &out{Body: Connect{MCPURL: base + "/mcp", GlossaryURL: base + "/glossary.md", OpenAPIURL: base + "/api/v1/openapi.json",
			DocsURL: base + "/api/docs", OAuth: on}}, nil
	})

	type appsOut struct {
		Body struct {
			Apps []ConnectedApp `json:"apps"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listConnectedApps", Method: http.MethodGet, Path: "/api/v1/connected-apps", Tags: []string{"agents"},
		Summary:     "List the MCP clients that you approved",
		Description: "Disconnect a client with the withdraw route of the OAuth provider.",
		Middlewares: huma.Middlewares{api.Require(auth.RoleMember)},
	}, func(ctx context.Context, _ *struct{}) (*appsOut, error) {
		rows, err := New(s.Pool).ConnectedApps(ctx, api.Caller(ctx).User.ID)
		if err != nil {
			return nil, api.Internal(err)
		}
		o := &appsOut{}
		o.Body.Apps = make([]ConnectedApp, 0, len(rows))
		for _, r := range rows {
			app := ConnectedApp{ClientID: r.ClientID, Name: r.Name, Host: r.Host, Kind: r.Kind, Scopes: strings.Fields(r.Scopes),
				ConnectedAt: r.UpdatedAt}
			if app.Host == "" {
				app.Host = hostOf(r.ClientID)
			}
			if app.Host == "" {
				first, _, _ := strings.Cut(r.RedirectUris, " ")
				app.Host = hostOf(first)
			}
			if app.Kind == "" {
				app.Kind = "dynamic"
				if hostOf(r.ClientID) != "" {
					app.Kind = "cimd"
				}
			}
			if app.Name == "" {
				app.Name = app.Host
			}
			o.Body.Apps = append(o.Body.Apps, app)
		}
		return o, nil
	})

	type statusOut struct{ Body OAuthStatus }
	huma.Register(a, huma.Operation{
		OperationID: "getMCPOAuth", Method: http.MethodGet, Path: "/api/v1/mcp/oauth", Tags: []string{"agents"},
		Summary:     "Get the state of OAuth sign-in of MCP clients",
		Middlewares: huma.Middlewares{api.Require(auth.RoleAdmin)},
	}, func(ctx context.Context, _ *struct{}) (*statusOut, error) {
		on, err := s.OAuthOn(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		st := OAuthStatus{On: on}
		if s.OAuthEnv != "" {
			st.LockedBy = EnvOAuth
		}
		return &statusOut{Body: st}, nil
	})
}
