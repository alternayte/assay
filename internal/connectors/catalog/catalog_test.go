package catalog_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/alternayte/assay/internal/connectors/catalog"
	"github.com/alternayte/assay/internal/content"
)

const file = `apiVersion: backstage.io/v1alpha1
kind: Component
metadata:
  name: payments-api
  title: Payments API
  annotations:
    github.com/project-slug: acme/payments-api
spec:
  type: service
  lifecycle: production
  owner: group:payments-team
  system: payments
  dependsOn: [resource:payments-db, component:ledger-service]
  providesApis:
    - payments-rest
  consumesApis:
    - ledger-rest
---
apiVersion: backstage.io/v1alpha1
kind: API
metadata:
  name: payments-rest
  annotations:
    backstage.io/source-location: url:https://ado.acme.test/tfs/Acme/Platform/_git/payments-contracts?path=/
spec:
  lifecycle: production
  owner: user:dana.acme
  system: payments
---
kind: System
metadata:
  name: payments
spec:
  owner: payments-team
---
kind: Group
metadata:
  name: payments-team
  namespace: finance
---
kind: Resource
metadata:
  name: payments-db
spec:
  owner: payments-team
  dependsOn:
    - resource:backups
---
kind: Domain
metadata:
  name: commerce
spec:
  owner: payments-team
---
kind: Location
metadata:
  name: ignored
`

func TestSCN_CON_006_CatalogKeysTypesAndClaims(t *testing.T) {
	repo := content.EntityRef{Type: "repository", Key: "github:github.com:acme/monorepo", Name: "monorepo"}
	facts, err := catalog.Facts([]byte(file), catalog.Repository{Ref: repo, GitHubHost: "github.com"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range facts {
		obj := f.Text
		if f.Object != nil {
			obj = f.Object.Type + " " + f.Object.Key
		}
		got = append(got, fmt.Sprintf("%s %s %s %s | %s", f.Subject.Type, f.Subject.Key, f.Predicate, obj, f.Quote))
		if f.Source != "catalog" {
			t.Fatalf("source %q", f.Source)
		}
	}
	want := []string{
		"service backstage:component:default/payments-api owned_by team backstage:group:default/payments-team | owner: group:payments-team",
		"service backstage:component:default/payments-api part_of system backstage:system:default/payments | system: payments",
		"service backstage:component:default/payments-api depends_on resource backstage:resource:default/payments-db | dependsOn: [resource:payments-db, component:ledger-service]",
		"service backstage:component:default/payments-api depends_on service backstage:component:default/ledger-service | dependsOn: [resource:payments-db, component:ledger-service]",
		"service backstage:component:default/payments-api provides_api api backstage:api:default/payments-rest | - payments-rest",
		"service backstage:component:default/payments-api consumes_api api backstage:api:default/ledger-rest | - ledger-rest",
		"service backstage:component:default/payments-api status production | lifecycle: production",
		"repository github:github.com:acme/payments-api implements service backstage:component:default/payments-api | github.com/project-slug: acme/payments-api",
		"api backstage:api:default/payments-rest owned_by person backstage:user:default/dana.acme | owner: user:dana.acme",
		"api backstage:api:default/payments-rest part_of system backstage:system:default/payments | system: payments",
		"repository azdo:ado.acme.test:platform/payments-contracts implements api backstage:api:default/payments-rest | backstage.io/source-location: url:https://ado.acme.test/tfs/Acme/Platform/_git/payments-contracts?path=/",
		"system backstage:system:default/payments owned_by team backstage:group:default/payments-team | owner: payments-team",
		"resource backstage:resource:default/payments-db owned_by team backstage:group:default/payments-team | owner: payments-team",
		"resource backstage:resource:default/payments-db depends_on resource backstage:resource:default/backups | - resource:backups",
		"system backstage:domain:default/commerce owned_by team backstage:group:default/payments-team | owner: payments-team",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("facts\n got %q\nwant %q", got, want)
	}
	if facts[0].Subject.Name != "Payments API" {
		t.Fatalf("name %q", facts[0].Subject.Name)
	}

	var keys []string
	for _, e := range catalog.Entities([]byte(file)) {
		keys = append(keys, e.Type+" "+e.Key)
	}
	wantKeys := []string{"service backstage:component:default/payments-api", "api backstage:api:default/payments-rest",
		"system backstage:system:default/payments", "team backstage:group:finance/payments-team",
		"resource backstage:resource:default/payments-db", "system backstage:domain:default/commerce"}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("entities %q", keys)
	}

	// Without an annotation, the repository of the file implements the
	// component.
	plain, err := catalog.Facts([]byte("kind: Component\nmetadata:\n  name: Reminders\n"), catalog.Repository{Ref: repo})
	if err != nil || len(plain) != 1 || plain[0].Subject.Key != repo.Key || plain[0].Object.Key != "backstage:component:default/reminders" ||
		plain[0].Quote != "name: Reminders" {
		t.Fatalf("plain %+v %v", plain, err)
	}
	if _, err := catalog.Facts([]byte("kind: [Component"), catalog.Repository{Ref: repo}); err == nil {
		t.Fatal("bad YAML gave no error")
	}
}
