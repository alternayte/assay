package design_test

import (
	"strings"
	"testing"
)

const paymentsSpec = `openapi: 3.0.3
info:
  title: Payments REST API
  version: 1.5.0
paths:
  /payments:
    post:
      summary: Take a card payment.
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/PaymentRequest'
      responses:
        '201':
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Payment'
  /payments/{paymentId}:
    get:
      summary: Read one payment.
components:
  schemas:
    PaymentRequest:
      type: object
      required: [amount]
      properties:
        amount:
          type: integer
        currency:
          type: string
    Payment:
      type: object
      properties:
        id:
          type: string
`

const paymentsEvents = `asyncapi: 2.6.0
info:
  title: Payments events
  version: 1.0.0
channels:
  payments.events:
    subscribe:
      message:
        name: PaymentCreated
        payload:
          type: object
          properties:
            paymentId:
              type: string
`

const ledgerProto = `syntax = "proto3";
package acme;

service Ledger {
  rpc Post(PostRequest) returns (PostReply);
}

message PostRequest {
  string account = 1;
  int64 amount = 2;
}

message PostReply {
  string entry_id = 1;
}
`

const paymentsRoutes = `package payments

import "net/http"

// Routes registers the payment routes.
func Routes(mux *http.ServeMux, h *Handlers) {
	mux.HandleFunc("POST /payments", h.Create)
	mux.HandleFunc("DELETE /payments/{id}", h.Cancel)
}
`

// evidence counts the evidence rows of a claim by source type.
func (w *world) evidence(t *testing.T, subject, predicate, object, source string) int {
	t.Helper()
	return w.env.Count(`SELECT count(*) FROM claim_evidence e JOIN claims c ON c.id = e.claim_id
JOIN entities s ON s.id = c.subject_id LEFT JOIN entities o ON o.id = c.object_entity_id
WHERE s.key = $1 AND c.predicate = $2 AND coalesce(o.key, c.object_text) = $3 AND e.source_type = $4 AND c.retracted_at IS NULL`,
		subject, predicate, object, source)
}

// TestSCN_DSG_010_ContractsFromSpecFilesAndCode syncs spec files and code
// of one repository. The spec facts cite their YAML lines, inference from
// the code meets them on the same operation key, and the design page lists
// the contracts.
func TestSCN_DSG_010_ContractsFromSpecFilesAndCode(t *testing.T) {
	t.Parallel()
	w := start(t)
	repo := repoKey(w.host, "acme/payments-api")
	w.src.PutFile("github", "acme/payments-api", "api/openapi.yaml", []byte(paymentsSpec))
	w.src.PutFile("github", "acme/payments-api", "api/asyncapi.yaml", []byte(paymentsEvents))
	w.src.PutFile("github", "acme/payments-api", "proto/ledger.proto", []byte(ledgerProto))
	w.src.PutFile("github", "acme/payments-api", "internal/payments/routes.go", []byte(paymentsRoutes))
	w.sync(t, w.github)
	w.build(t, w.github)

	api := "api:" + repo + ":api/openapi.yaml"
	post := "operation:" + repo + ":POST /payments"
	if n := w.evidence(t, repo, "provides_api", api, "repository"); n != 1 {
		t.Fatalf("provides_api of the spec api has %d evidence rows", n)
	}
	if n := w.evidence(t, api, "has_operation", post, "repository"); n != 1 {
		t.Fatalf("has_operation from the spec has %d evidence rows", n)
	}
	// The spec fact cites the passage that holds its line.
	var quote, text string
	if err := w.env.Server.Pool().QueryRow(t.Context(), `SELECT e.quote, p.text FROM claim_evidence e JOIN claims c ON c.id = e.claim_id
JOIN entities o ON o.id = c.object_entity_id JOIN passages p ON p.item_id = e.item_id AND p.hash = e.passage_hash
WHERE o.key = $1 AND c.predicate = 'has_operation' AND e.source_type = 'repository'`, post).Scan(&quote, &text); err != nil {
		t.Fatal(err)
	}
	if quote != "post:" || !strings.Contains(text, "/payments:") {
		t.Fatalf("the spec fact quotes %q in %q", quote, text)
	}
	request := "data_model:" + repo + ":PaymentRequest"
	if n := w.evidence(t, post, "request", request, "repository"); n != 1 {
		t.Fatalf("the request model has %d evidence rows", n)
	}
	if n := w.evidence(t, request, "has_field", "amount: integer (required)", "repository"); n != 1 {
		t.Fatal("the request model has no required amount field")
	}
	if n := w.evidence(t, post, "response", "data_model:"+repo+":Payment", "repository"); n != 1 {
		t.Fatal("the operation has no response model")
	}

	// The handler states the same operation: one entity with both sources.
	if n := w.evidence(t, api, "has_operation", post, "decision"); n != 1 {
		t.Fatalf("has_operation inferred from code has %d evidence rows on the spec api", n)
	}
	if n := w.env.Count("SELECT count(*) FROM entities WHERE key = $1", post); n != 1 {
		t.Fatalf("%d entities for POST /payments", n)
	}
	// An operation only in the code attaches to the spec api of its protocol.
	if n := w.evidence(t, api, "has_operation", "operation:"+repo+":DELETE /payments/{}", "decision"); n != 1 {
		t.Fatal("the code-only operation is not on the spec api")
	}

	event := "event:" + repo + ":payments.events:PaymentCreated"
	if n := w.evidence(t, repo, "publishes", event, "repository"); n != 1 {
		t.Fatal("the AsyncAPI subscribe operation gives no publishes")
	}
	if n := w.evidence(t, event, "channel", "payments.events", "repository"); n != 1 {
		t.Fatal("the event has no channel")
	}
	if n := w.evidence(t, event, "payload", "data_model:"+repo+":PaymentCreated payload", "repository"); n != 1 {
		t.Fatal("the event has no payload model")
	}
	rpc := "operation:" + repo + ":acme.Ledger/Post"
	if n := w.evidence(t, "api:"+repo+":proto/ledger.proto", "has_operation", rpc, "repository"); n != 1 {
		t.Fatal("the proto service gives no operation")
	}

	// The design page lists the contracts, each with a cite.
	sec := find(get(t, w.member, repo), "contracts")
	if sec.Title != "Contracts" || !strings.Contains(sec.Markdown, "POST /payments") || !strings.Contains(sec.Markdown, "acme.Ledger/Post") {
		t.Fatalf("contracts section %+v", sec)
	}
	cited(t, sec)

	// A spec file without the operation retracts its spec facts.
	w.src.PutFile("github", "acme/payments-api", "api/openapi.yaml", []byte(strings.Replace(paymentsSpec, `  /payments/{paymentId}:
    get:
      summary: Read one payment.
`, "", 1)))
	w.sync(t, w.github)
	gone := "operation:" + repo + ":GET /payments/{}"
	if n := w.evidence(t, api, "has_operation", gone, "repository"); n != 0 {
		t.Fatalf("the removed operation keeps %d spec evidence rows", n)
	}
	if n := w.env.Count(`SELECT count(*) FROM claims c JOIN entities o ON o.id = c.object_entity_id
WHERE o.key = $1 AND c.retracted_at IS NOT NULL`, gone); n != 1 {
		t.Fatal("the claim of the removed operation is not retracted")
	}
	if n := w.evidence(t, api, "has_operation", post, "repository"); n != 1 {
		t.Fatal("the kept operation lost its spec evidence")
	}
}
