package contracts_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/connectors/contracts"
	"github.com/alternayte/assay/internal/content"
)

var repo = content.EntityRef{Type: "repository", Key: "github:git.acme.test:acme/payments-api", Name: "payments-api"}

// facts returns the facts of a file as "subject predicate object @line".
func facts(t *testing.T, file, src string) []string {
	t.Helper()
	res, err := contracts.Parse(file, []byte(src), repo)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	lines := strings.Split(src, "\n")
	for _, f := range res.Facts {
		obj := f.Text
		if f.Object != nil {
			obj = strings.TrimPrefix(f.Object.Key, strings.SplitN(f.Object.Key, ":", 2)[0]+":"+repo.Key+":")
		}
		subj := strings.TrimPrefix(f.Subject.Key, strings.SplitN(f.Subject.Key, ":", 2)[0]+":"+repo.Key+":")
		if f.Line < 1 || !strings.Contains(lines[f.Line-1], f.Quote) {
			t.Errorf("%s %s: quote %q is not line %d", subj, f.Predicate, f.Quote, f.Line)
		}
		out = append(out, fmt.Sprintf("%s %s %s @%d", subj, f.Predicate, obj, f.Line))
	}
	return out
}

func TestSCN_CON_009_SpecFilesGiveContracts(t *testing.T) {
	cases := []struct {
		file, src string
		want      []string
	}{
		{"api/schema.graphql", "type Query {\n  payment(id: ID!): Payment\n}\n\ntype Payment {\n  id: ID!\n  amount: Int\n}\n", []string{
			"github:git.acme.test:acme/payments-api provides_api api/schema.graphql @1",
			"api/schema.graphql has_operation graphql:Query.payment @2",
			"graphql:Query.payment response Payment @2",
			"Payment has_field id: ID! (required) @6",
			"Payment has_field amount: Int @7",
		}},
		{"events/payment.avsc", "{\n  \"type\": \"record\",\n  \"name\": \"PaymentCreated\",\n  \"namespace\": \"acme.events\",\n  \"fields\": [\n" +
			"    {\"name\": \"id\", \"type\": \"string\"},\n    {\"name\": \"note\", \"type\": [\"null\", \"string\"]}\n  ]\n}\n", []string{
			"acme.events.PaymentCreated has_field id: string @6",
			"acme.events.PaymentCreated has_field note: null|string @7",
		}},
		{"api/events.yaml", "asyncapi: 3.0.0\ninfo:\n  title: Ledger events\nchannels:\n  entries:\n    address: ledger.entries\n    messages:\n" +
			"      EntryPosted:\n        payload:\n          type: object\n          properties:\n            entryId:\n              type: string\n" +
			"operations:\n  onEntry:\n    action: receive\n    channel:\n      $ref: '#/channels/entries'\n", []string{
			"github:git.acme.test:acme/payments-api provides_api api/events.yaml @3",
			"github:git.acme.test:acme/payments-api subscribes_to ledger.entries:EntryPosted @16",
			"ledger.entries:EntryPosted channel ledger.entries @5",
			"EntryPosted payload has_field entryId: string @12",
			"ledger.entries:EntryPosted payload EntryPosted payload @9",
		}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			got := facts(t, c.file, c.src)
			for _, w := range c.want {
				if !slices.Contains(got, w) {
					t.Errorf("missing %q in\n%s", w, strings.Join(got, "\n"))
				}
			}
		})
	}

	// api.yaml is a spec file by its top-level key. Another YAML file is not.
	if contracts.Detect("deploy/api.yaml", []byte("openapi: 3.1.0\ninfo:\n  title: X\n")) != contracts.OpenAPI {
		t.Error("api.yaml with an openapi key is not detected")
	}
	if contracts.Detect("Chart.yaml", []byte("name: payments\n")) != "" {
		t.Error("Chart.yaml is detected as a spec file")
	}

	// A remote $ref gives a model with the ref as its name and no fields.
	remote := facts(t, "openapi.yaml", "openapi: 3.0.0\ninfo:\n  title: X\npaths:\n  /a:\n    get:\n      responses:\n        '200':\n"+
		"          content:\n            application/json:\n              schema:\n                $ref: 'https://schemas.acme.test/a.json'\n")
	if !slices.Contains(remote, "GET /a response https://schemas.acme.test/a.json @8") {
		t.Errorf("remote ref: %v", remote)
	}

	// A file past the operation limit stops at the limit with a skip reason.
	var b strings.Builder
	b.WriteString("openapi: 3.0.0\ninfo:\n  title: Big\npaths:\n")
	for i := range contracts.MaxOperations + 5 {
		fmt.Fprintf(&b, "  /r%d:\n    get:\n      summary: r\n", i)
	}
	res, err := contracts.Parse("big/openapi.yaml", []byte(b.String()), repo)
	if err != nil {
		t.Fatal(err)
	}
	ops := 0
	for _, f := range res.Facts {
		if f.Predicate == "has_operation" {
			ops++
		}
	}
	if ops != contracts.MaxOperations || res.Skip != contracts.SkipLimit {
		t.Fatalf("%d operations, skip %q", ops, res.Skip)
	}
}
