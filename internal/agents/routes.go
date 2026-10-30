package agents

import (
	"context"
	"net/http"
	"strings"

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
}

// Register adds the route that the Connect an agent page reads.
func (s *Service) Register(a huma.API) {
	type out struct{ Body Connect }
	huma.Register(a, huma.Operation{
		OperationID: "getConnect", Method: http.MethodGet, Path: "/api/v1/connect", Tags: []string{"agents"},
		Summary:     "Get the agent URLs of this instance",
		Description: "The MCP endpoint, the glossary Markdown, the OpenAPI document and the API reference.",
		Middlewares: huma.Middlewares{api.Require(auth.RoleMember)},
	}, func(context.Context, *struct{}) (*out, error) {
		base := strings.TrimRight(s.BaseURL, "/")
		return &out{Body: Connect{MCPURL: base + "/mcp", GlossaryURL: base + "/glossary.md", OpenAPIURL: base + "/api/v1/openapi.json",
			DocsURL: base + "/api/docs"}}, nil
	})
}
