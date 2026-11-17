package provider_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	provider "github.com/openai/terraform-provider-openai/internal/provider"
)

// These creates allocate a new server-side object and the API has no
// idempotency-key contract. A create that fails ambiguously (5xx, 408, 409,
// 429, dropped connection) may already have been committed, so repeating the
// POST could create an untracked duplicate.
func TestNonIdempotentCreatesAreNotRetried(t *testing.T) {
	cases := []struct {
		resourceType string
		createPath   string
		config       string
	}{
		{
			resourceType: "openai_certificate",
			createPath:   "/v1/organization/certificates",
			config: `
resource "openai_certificate" "test" {
  name        = "test"
  certificate = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
}`,
		},
		{
			resourceType: "openai_group",
			createPath:   "/v1/organization/groups",
			config: `
resource "openai_group" "test" {
  name = "test"
}`,
		},
		{
			resourceType: "openai_invite",
			createPath:   "/v1/organization/invites",
			config: `
resource "openai_invite" "test" {
  email = "user@example.invalid"
  role  = "reader"
}`,
		},
		{
			resourceType: "openai_organization_spend_alert",
			createPath:   "/v1/organization/spend_alerts",
			config: `
resource "openai_organization_spend_alert" "test" {
  threshold_amount                = 1000
  currency                        = "USD"
  interval                        = "month"
  notification_channel_type       = "email"
  notification_channel_recipients = ["alerts@example.invalid"]
}`,
		},
		{
			resourceType: "openai_project_spend_alert",
			createPath:   "/v1/organization/projects/proj_test/spend_alerts",
			config: `
resource "openai_project_spend_alert" "test" {
  project_id                      = "proj_test"
  threshold_amount                = 1000
  currency                        = "USD"
  interval                        = "month"
  notification_channel_type       = "email"
  notification_channel_recipients = ["alerts@example.invalid"]
}`,
		},
		{
			resourceType: "openai_project",
			createPath:   "/v1/organization/projects",
			config: `
resource "openai_project" "test" {
  name = "test"
}`,
		},
		{
			resourceType: "openai_project_role",
			createPath:   "/v1/projects/proj_test/roles",
			config: `
resource "openai_project_role" "test" {
  project_id  = "proj_test"
  role_name   = "test"
  permissions = ["api.project.read"]
}`,
		},
		{
			resourceType: "openai_role",
			createPath:   "/v1/organization/roles",
			config: `
resource "openai_role" "test" {
  role_name   = "test"
  permissions = ["api.organization.read"]
}`,
		},
		{
			resourceType: "openai_project_service_account",
			createPath:   "/v1/organization/projects/proj_test/service_accounts",
			config: `
resource "openai_project_service_account" "test" {
  project_id = "proj_test"
  name       = "test"
}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.resourceType, func(t *testing.T) {
			var mu sync.Mutex
			createAttempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if req.Method == http.MethodPost && req.URL.Path == tc.createPath {
					mu.Lock()
					createAttempts++
					mu.Unlock()
					// The gateway reports a failure even though the create may have been committed.
					w.WriteHeader(http.StatusBadGateway)
					_, _ = w.Write([]byte(`{"error":{"message":"upstream failure","type":"server_error"}}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"message":"unexpected request","type":"invalid_request_error"}}`))
			}))
			t.Cleanup(server.Close)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
					"openai": providerserver.NewProtocol6WithError(provider.New("test")()),
				},
				Steps: []resource.TestStep{
					{
						Config: fmt.Sprintf(`
provider "openai" {
  admin_api_key = "test"
  base_url      = %q
}
`, server.URL+"/v1") + tc.config,
						ExpectError: regexp.MustCompile(`502`),
					},
				},
			})

			mu.Lock()
			defer mu.Unlock()
			if createAttempts != 1 {
				t.Fatalf("expected exactly one create attempt for %s, got %d", tc.resourceType, createAttempts)
			}
		})
	}
}
