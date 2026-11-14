package workloadidentityprovider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	provider "github.com/openai/terraform-provider-openai/internal/provider"
)

// newWorkloadIdentityDefaultsServer returns a mock Admin API that fills in the
// server-default verification and permissions objects when a request omits them,
// and advances updated_at on every update.
func newWorkloadIdentityDefaultsServer(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string]map[string]any{}
	updates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		const providers = "/v1/organization/workload_identity_providers"
		switch req.Method {
		case http.MethodPost:
			var body map[string]any
			decoder := json.NewDecoder(req.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			key := req.URL.Path
			switch {
			case req.URL.Path == providers:
				key = providers + "/wip_test"
			case strings.HasSuffix(req.URL.Path, "/federation_rules"):
				key = req.URL.Path + "/wifr_test"
			}
			object := objects[key]
			if object == nil {
				object = map[string]any{"description": nil, "enabled": true, "created_at": 1, "updated_at": 1}
				if strings.HasSuffix(key, "/wip_test") {
					object["id"] = "wip_test"
					object["type"] = "oidc"
					object["verification"] = map[string]any{"type": "discovery", "discovery_url": nil}
					object["attribute_transformations"] = []any{}
				} else {
					object["id"] = "wifr_test"
					object["workload_identity_provider_id"] = "wip_test"
					object["permissions"] = map[string]any{"type": "service_account_default"}
				}
				objects[key] = object
			} else {
				updates++
				object["updated_at"] = 1 + updates
			}
			for name, value := range body {
				object[name] = value
			}
			_ = json.NewEncoder(w).Encode(object)
		case http.MethodGet:
			if object := objects[req.URL.Path]; object != nil {
				_ = json.NewEncoder(w).Encode(object)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found","type":"invalid_request_error"}}`))
		case http.MethodDelete:
			delete(objects, req.URL.Path)
			_, _ = w.Write([]byte(`{"deleted":true}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return updates
	}
}

func workloadIdentityDefaultsConfig(baseURL string, providerName string) string {
	return fmt.Sprintf(`
provider "openai" {
  admin_api_key = "test"
  base_url      = %q
}

resource "openai_workload_identity_provider" "example" {
  name                      = %q
  issuer                    = "https://issuer.example.com"
  audience                  = "openai"
  attribute_transformations = []
}

resource "openai_workload_identity_federation_rule" "example" {
  workload_identity_provider_id = openai_workload_identity_provider.example.id
  project_id                    = "proj_123"
  service_account_id            = "service_account_123"
  attribute_matchers            = { repository = "openai/example", protected = true }
}
`, baseURL+"/v1", providerName)
}

// The documented examples omit verification and permissions, which the API
// fills in with server defaults. They must converge to an empty plan, while a
// real update still refreshes server-computed values such as updated_at.
func TestWorkloadIdentityServerDefaultsConvergeToEmptyPlan(t *testing.T) {
	server, updates := newWorkloadIdentityDefaultsServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"openai": providerserver.NewProtocol6WithError(provider.New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config: workloadIdentityDefaultsConfig(server.URL, "terraform-managed"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("openai_workload_identity_provider.example", "verification.type", "discovery"),
					resource.TestCheckResourceAttr("openai_workload_identity_federation_rule.example", "permissions.type", "service_account_default"),
				),
			},
			{
				Config:   workloadIdentityDefaultsConfig(server.URL, "terraform-managed"),
				PlanOnly: true,
			},
			{
				Config: workloadIdentityDefaultsConfig(server.URL, "terraform-managed-renamed"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("openai_workload_identity_provider.example", "name", "terraform-managed-renamed"),
					resource.TestCheckResourceAttr("openai_workload_identity_provider.example", "updated_at", "2"),
				),
			},
			{
				Config:   workloadIdentityDefaultsConfig(server.URL, "terraform-managed-renamed"),
				PlanOnly: true,
			},
		},
	})

	if got := updates(); got != 1 {
		t.Fatalf("expected exactly one update request for the rename, got %d", got)
	}
}
