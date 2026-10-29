package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakePolicyAPI serves one Policy and counts the requests that send
// postures, since each one revokes the Policy's authorizations.
type fakePolicyAPI struct {
	mu            sync.Mutex
	policy        map[string]any
	postureWrites int
}

const fakePolicyID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func (f *fakePolicyAPI) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		var body struct {
			Policy map[string]json.RawMessage `json:"policy"`
		}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("decoding %s %s: %v", r.Method, r.URL.Path, err)
				}
			}
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/policies":
			f.policy = map[string]any{
				"id": fakePolicyID, "description": "", "is_disabled": false,
				"flow_log_uploads_enabled": true, "require_attestation": false, "conditions": []any{},
			}
			f.apply(t, body.Policy)
		case r.Method == http.MethodPatch && r.URL.Path == "/policies/"+fakePolicyID:
			f.apply(t, body.Policy)
		case r.Method == http.MethodGet && r.URL.Path == "/policies/"+fakePolicyID:
		case r.Method == http.MethodDelete && r.URL.Path == "/policies/"+fakePolicyID:
			f.policy = nil
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if f.policy == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": f.policy})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// apply stores the fields a request sent. Postures come back with every
// integration check's rows spelled out, as a server may return the
// default it applied - the provider must not read that as drift.
func (f *fakePolicyAPI) apply(t *testing.T, fields map[string]json.RawMessage) {
	for k, raw := range fields {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Errorf("decoding %s: %v", k, err)
		}
		if k == "postures" {
			f.postureWrites++
			fakeSpellOutRows(v)
		}
		f.policy[k] = v
	}
}

func fakeSpellOutRows(v any) {
	switch n := v.(type) {
	case map[string]any:
		if field, ok := n["field"].(string); ok && n["rows"] == nil && !strings.HasPrefix(field, "firezone.") {
			n["rows"] = "any"
		}
		for _, child := range n {
			fakeSpellOutRows(child)
		}
	case []any:
		for _, child := range n {
			fakeSpellOutRows(child)
		}
	}
}

func (f *fakePolicyAPI) checkWrites(want int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.postureWrites != want {
			return fmt.Errorf("postures sent %d times, want %d", f.postureWrites, want)
		}
		return nil
	}
}

func (f *fakePolicyAPI) checkPosture(want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		got, _ := json.Marshal(f.policy["postures"])
		if string(got) != want {
			return fmt.Errorf("server postures\n got: %s\nwant: %s", got, want)
		}
		return nil
	}
}

// TestAccPolicyResource_PostureLifecycle runs the posture attribute
// through real Terraform against a fake API: create, a respelling that
// must not resend the posture, import, a change, and removal.
func TestAccPolicyResource_PostureLifecycle(t *testing.T) {
	api := &fakePolicyAPI{}
	srv := api.serve(t)

	config := func(posture string) string {
		return fmt.Sprintf(`
provider "firezone" {
  endpoint = %q
  token    = "test-token"
}

locals {
  managed = { field = "intune.enrolled", is = true }
}

resource "firezone_policy" "test" {
  group_id    = "11111111-1111-1111-1111-111111111111"
  resource_id = "22222222-2222-2222-2222-222222222222"
  %s
}
`, srv.URL, posture)
	}

	// Lists through tolist(), rows = "any" spelled out and a check
	// shared through a local: none of it is how Read spells the tree.
	respelled := config(`posture = {
    all = [
      local.managed,
      { field = "intune.compliance_state", is_in = tolist(["compliant"]), rows = "any" },
      { any = [
        { field = "defender.os_build", gte = 40.0 },
        { field = "firezone.last_seen_version", gte = "@latest" },
      ] },
      { not = { field = "sentinelone.infected", is = true } },
    ]
  }`)
	canonical := func(build int) string {
		return config(fmt.Sprintf(`posture = {
    all = [
      { field = "intune.enrolled", is = true },
      { field = "intune.compliance_state", is_in = ["compliant"] },
      { any = [
        { field = "defender.os_build", gte = %d },
        { field = "firezone.last_seen_version", gte = "@latest" },
      ] },
      { not = { field = "sentinelone.infected", is = true } },
    ]
  }`, build))
	}
	wire := func(build int) string {
		return `{"and":[` +
			`{"field":"intune.enrolled","op":"is","rows":"any","value":true},` +
			`{"field":"intune.compliance_state","op":"is_in","rows":"any","value":["compliant"]},` +
			fmt.Sprintf(`{"or":[{"field":"defender.os_build","op":"gte","rows":"any","value":%d},`, build) +
			`{"field":"firezone.last_seen_version","op":"gte","value":"@latest"}]},` +
			`{"not":{"field":"sentinelone.infected","op":"is","rows":"any","value":true}}]}`
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: respelled,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.all.1.field", "intune.compliance_state"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.all.1.is_in.0", "compliant"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.all.2.any.0.gte", "40"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.all.3.not.is", "true"),
					api.checkWrites(1),
					api.checkPosture(wire(40)),
				),
			},
			// The same expression spelled the way Read spells it plans an
			// update, as the config text changed, but must not send the
			// posture again.
			{
				Config: canonical(40),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("firezone_policy.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: api.checkWrites(1),
			},
			{
				ResourceName:      "firezone_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: canonical(41),
				Check: resource.ComposeAggregateTestCheckFunc(
					api.checkWrites(2),
					api.checkPosture(wire(41)),
				),
			},
			{
				Config: config(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					api.checkWrites(3),
					api.checkPosture("null"),
					resource.TestCheckNoResourceAttr("firezone_policy.test", "posture"),
				),
			},
		},
	})
}

// TestAccPolicyResource_PostureUpgradeFromV03 applies a posture block
// with the released 0.3.0 provider, then plans the same posture written
// as the attribute with this one. The state upgrade must leave nothing to
// change, so nothing is sent. Downloads 0.3.0 from the Terraform Registry.
func TestAccPolicyResource_PostureUpgradeFromV03(t *testing.T) {
	api := &fakePolicyAPI{}
	srv := api.serve(t)

	config := func(posture string) string {
		return fmt.Sprintf(`
provider "firezone" {
  endpoint = %q
  token    = "test-token"
}

resource "firezone_policy" "test" {
  group_id    = "11111111-1111-1111-1111-111111111111"
  resource_id = "22222222-2222-2222-2222-222222222222"
  %s
}
`, srv.URL, posture)
	}

	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ExternalProviders: map[string]resource.ExternalProvider{
					"firezone": {Source: "firezone/firezone", VersionConstraint: "0.3.0"},
				},
				Config: config(`
  posture {
    operator = "and"

    check {
      field    = "intune.enrolled"
      operator = "is"
      value    = "true"
    }

    group {
      operator = "or"
      negate   = true

      check {
        field    = "intune.compliance_state"
        operator = "is_in"
        values   = ["compliant"]
        rows     = "all"
      }

      check {
        field    = "defender.os_build"
        operator = "gt"
        value    = "40"
      }
    }
  }`),
				Check: api.checkWrites(1),
			},
			{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Config: config(`
  posture = {
    all = [
      { field = "intune.enrolled", is = true },
      { not = { any = [
        { field = "intune.compliance_state", is_in = ["compliant"], rows = "all" },
        { field = "defender.os_build", gt = 40 },
      ] } },
    ]
  }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("firezone_policy.test", plancheck.ResourceActionNoop),
					},
				},
				Check: api.checkWrites(1),
			},
		},
	})
}

// TestAccPolicyResource_PostureInvalidPlan checks a mistake is reported
// at plan time, naming the spot inside the expression.
func TestAccPolicyResource_PostureInvalidPlan(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "firezone" {
  endpoint = "http://127.0.0.1:1"
  token    = "test-token"
}

resource "firezone_policy" "test" {
  group_id    = "11111111-1111-1111-1111-111111111111"
  resource_id = "22222222-2222-2222-2222-222222222222"
  posture = {
    any = [
      { field = "intune.enrolled", is = true },
      { field = "intune.enrolled", is = "true" },
    ]
  }
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`posture.any\[1\].is: expected true or false`),
			},
		},
	})
}
