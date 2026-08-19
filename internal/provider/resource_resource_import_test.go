package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccResourceResource_ImportBlockAdoptsDevicePool checks that a
// config-driven `import` block adopts an existing static_device_pool
// Resource cleanly, despite ModifyPlan rejecting creation of that type.
//
// ModifyPlan returns early when prior state is non-null, and an import
// block populates prior state before the plan is computed, so the guard
// never fires on adoption. That is what makes the pool type importable
// at all - the auto-discovery generator emits device pools as resource
// blocks on the strength of it, so a regression here would silently
// break generated config.
//
// Unlike the other acceptance tests in this package, this one runs
// against an httptest fake rather than a live dev server: it needs a
// pool that already exists, and the API refuses to create one. It needs
// TF_ACC=1 and a terraform binary, but no FIREZONE_ENDPOINT.
func TestAccResourceResource_ImportBlockAdoptsDevicePool(t *testing.T) {
	const poolID = "11111111-2222-3333-4444-555555555555"

	pool := map[string]any{
		"id":                  poolID,
		"name":                "office-devices",
		"type":                "static_device_pool",
		"address":             nil,
		"address_description": nil,
		"ip_stack":            nil,
		"site_id":             nil,
		"filters":             []any{},
	}

	var deleted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/resources/"+poolID:
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": pool})
		case r.Method == http.MethodDelete && r.URL.Path == "/resources/"+poolID:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	config := fmt.Sprintf(`
provider "firezone" {
  endpoint = %q
  token    = "spike-token"
}

import {
  to = firezone_resource.pool
  id = %q
}

resource "firezone_resource" "pool" {
  name = "office-devices"
  type = "static_device_pool"
}
`, srv.URL, poolID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("firezone_resource.pool", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_resource.pool", "id", poolID),
					resource.TestCheckResourceAttr("firezone_resource.pool", "type", "static_device_pool"),
					resource.TestCheckNoResourceAttr("firezone_resource.pool", "site_id"),
					resource.TestCheckNoResourceAttr("firezone_resource.pool", "address"),
				),
			},
			// Re-running the identical config must produce no diff. The
			// framework fails the step if the post-apply plan is dirty.
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
