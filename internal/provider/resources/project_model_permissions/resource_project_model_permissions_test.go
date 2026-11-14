package projectmodelpermissions_test

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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	provider "github.com/openai/terraform-provider-openai/internal/provider"
)

type modelPermissionsPolicy struct {
	Mode     string   `json:"mode"`
	ModelIDs []string `json:"model_ids"`
}

type modelPermissionsServerState struct {
	mu       sync.Mutex
	policies map[string]*modelPermissionsPolicy
	requests []string
}

func newModelPermissionsServer(t *testing.T) (*httptest.Server, *modelPermissionsServerState) {
	t.Helper()
	state := &modelPermissionsServerState{policies: map[string]*modelPermissionsPolicy{}}
	server := httptest.NewServer(http.HandlerFunc(state.handle))
	t.Cleanup(server.Close)
	return server, state
}

func (s *modelPermissionsServerState) handle(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	projectPath := strings.TrimPrefix(req.URL.Path, "/v1/organization/projects/")
	projectID, ok := strings.CutSuffix(projectPath, "/model_permissions")
	if !ok {
		http.Error(w, `{"error":{"message":"unexpected path"}}`, http.StatusNotFound)
		return
	}
	s.requests = append(s.requests, req.Method+" "+projectID)
	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case http.MethodPost:
		var body modelPermissionsPolicy
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.policies[projectID] = &body
	case http.MethodDelete:
		delete(s.policies, projectID)
		_, _ = w.Write([]byte(`{"object":"project.model_permissions.deleted","deleted":true}`))
		return
	}

	policy := s.policies[projectID]
	if policy == nil {
		policy = &modelPermissionsPolicy{Mode: "deny_list", ModelIDs: []string{}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object":    "project.model_permissions",
		"mode":      policy.Mode,
		"model_ids": policy.ModelIDs,
	})
}

func (s *modelPermissionsServerState) deleteRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, request := range s.requests {
		if strings.HasPrefix(request, http.MethodDelete+" ") {
			count++
		}
	}
	return count
}

func modelPermissionsProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"openai": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

func modelPermissionsConfig(baseURL string, createBeforeDestroy bool) string {
	lifecycle := ""
	if createBeforeDestroy {
		lifecycle = "lifecycle {\n    create_before_destroy = true\n  }"
	}
	return fmt.Sprintf(`
provider "openai" {
  admin_api_key = "test"
  base_url      = %q
}

resource "openai_project_model_permissions" "test" {
  %s
  project_id = "proj_shared"
  mode       = "allow_list"
  model_ids  = ["gpt-4o-mini"]
}
`, baseURL+"/v1", lifecycle)
}

func TestModelPermissionsProtocolSameProjectCreateBeforeDestroy(t *testing.T) {
	server, serverState := newModelPermissionsServer(t)
	config := modelPermissionsConfig(server.URL, true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: modelPermissionsProviderFactories(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config: config,
				Taint:  []string{"openai_project_model_permissions.test"},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("openai_project_model_permissions.test", "mode", "allow_list"),
					func(_ *terraform.State) error {
						serverState.mu.Lock()
						defer serverState.mu.Unlock()
						policy := serverState.policies["proj_shared"]
						if policy == nil || policy.Mode != "allow_list" {
							return fmt.Errorf("deposed resource reset the live replacement's model permissions: %v", serverState.requests)
						}
						return nil
					},
				),
			},
		},
	})

	if deletes := serverState.deleteRequests(); deletes != 0 {
		t.Fatalf("expected no DELETE requests, got %d: %v", deletes, serverState.requests)
	}
}

func TestModelPermissionsDestroyLeavesProjectPolicy(t *testing.T) {
	server, serverState := newModelPermissionsServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: modelPermissionsProviderFactories(),
		Steps: []resource.TestStep{
			{Config: modelPermissionsConfig(server.URL, false)},
		},
		CheckDestroy: func(_ *terraform.State) error {
			serverState.mu.Lock()
			defer serverState.mu.Unlock()
			if serverState.policies["proj_shared"] == nil {
				return fmt.Errorf("destroy removed the project model permission policy: %v", serverState.requests)
			}
			return nil
		},
	})

	if deletes := serverState.deleteRequests(); deletes != 0 {
		t.Fatalf("expected destroy to forget state without a DELETE request, got %d: %v", deletes, serverState.requests)
	}
}
