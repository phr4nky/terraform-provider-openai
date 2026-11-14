package certificate_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	provider "github.com/openai/terraform-provider-openai/internal/provider"
)

const testCertificatePEM = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"

type certificateServerState struct {
	mu             sync.Mutex
	contentQueries int
	plainGets      int
	deletes        int
}

func newCertificateServer(t *testing.T) (*httptest.Server, *certificateServerState) {
	t.Helper()
	state := &certificateServerState{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Path != "/v1/organization/certificates/cert_existing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"unexpected request","type":"invalid_request_error"}}`))
			return
		}
		if req.Method == http.MethodDelete {
			state.deletes++
			_, _ = w.Write([]byte(`{"object":"certificate.deleted","id":"cert_existing"}`))
			return
		}
		details := map[string]any{"valid_at": 1700000000, "expires_at": 1800000000}
		if req.URL.Query().Get("include[]") == "content" {
			state.contentQueries++
			details["content"] = testCertificatePEM
		} else {
			state.plainGets++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object":              "certificate",
			"id":                  "cert_existing",
			"name":                "imported",
			"created_at":          1700000000,
			"certificate_details": details,
		})
	}))
	t.Cleanup(server.Close)
	return server, state
}

func TestCertificateImportReadsContentAndPlansNoChange(t *testing.T) {
	server, serverState := newCertificateServer(t)

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

import {
  to = openai_certificate.test
  id = "cert_existing"
}

resource "openai_certificate" "test" {
  name        = "imported"
  certificate = %q
}
`, server.URL+"/v1", testCertificatePEM),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("openai_certificate.test", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.TestCheckResourceAttr("openai_certificate.test", "certificate_id", "cert_existing"),
			},
		},
	})

	serverState.mu.Lock()
	defer serverState.mu.Unlock()
	if serverState.contentQueries == 0 {
		t.Fatal("expected import to request the certificate content")
	}
	if serverState.plainGets == 0 {
		t.Fatal("expected refreshes after import to read without requesting the certificate content")
	}
}
