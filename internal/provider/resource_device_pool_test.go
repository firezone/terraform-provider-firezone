package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Exercises real Terraform planning/state checks using the Elixir API's wire
// shapes. No API credentials or live account are needed.
func TestAccResourceDevicePoolLifecycle(t *testing.T) {
	const poolID = "11111111-2222-3333-4444-555555555555"
	const deviceID = "22222222-2222-3333-4444-555555555555"
	const groupID = "33333333-2222-3333-4444-555555555555"
	var mu sync.Mutex
	var pool map[string]any
	var preserveCriteria bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost, http.MethodPatch:
			if r.Method == http.MethodPost && r.URL.Path != "/resources" || r.Method == http.MethodPatch && r.URL.Path != "/resources/"+poolID {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			var body struct {
				Resource map[string]any `json:"resource"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.Method == http.MethodPost {
				pool = map[string]any{"id": poolID, "filters": []any{}}
			}
			criteria, hasCriteria := body.Resource["device_membership_criteria"]
			if preserveCriteria && hasCriteria {
				t.Error("unconfigured criteria must not be sent on update")
			}
			for k, v := range body.Resource {
				pool[k] = v
			}
			if pool["type"] == "device_pool" {
				if pool["device_membership_criteria"] == nil {
					t.Error("device pool missing required criteria")
					w.WriteHeader(422)
					return
				}
				pool["address"] = nil
				pool["site_id"] = nil
				pool["ip_stack"] = nil
				if hasCriteria {
					// The server normalizes listed IDs. Terraform sets must tolerate it.
					rules := criteria.(map[string]any)
					if rule, ok := rules["device"].(map[string]any); ok && rule["op"] == "in" {
						ids := rule["value"].([]any)
						sort.Slice(ids, func(i, j int) bool { return ids[i].(string) < ids[j].(string) })
					}
				}
			} else {
				pool["device_membership_criteria"] = nil
			}
		case http.MethodGet:
			if pool == nil {
				w.WriteHeader(404)
				return
			}
		case http.MethodDelete:
			pool = nil
			w.WriteHeader(204)
			return
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": pool})
	}))
	defer srv.Close()
	config := func(name, extra string) string {
		return fmt.Sprintf(`
provider "firezone" {
 endpoint = %q
 token = "test-token"
}
resource "firezone_resource" "pool" {
 name = %q
 type = "device_pool"
 %s
}`, srv.URL, name, extra)
	}
	criteria := func(mode, extra string) string {
		return fmt.Sprintf(`device_membership_criteria = { mode = %q
%s
}`, mode, extra)
	}
	checkMode := func(mode string) resource.TestCheckFunc {
		return resource.TestCheckResourceAttr("firezone_resource.pool", "device_membership_criteria.mode", mode)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("empty", ""), Check: checkMode("listed")},
			{ResourceName: "firezone_resource.pool", ImportState: true, ImportStateVerify: true},
			{Config: config("own", criteria("own_devices", "")), Check: checkMode("own_devices")},
			{ResourceName: "firezone_resource.pool", ImportState: true, ImportStateVerify: true},
			{Config: config("all", criteria("all_devices", "")), Check: checkMode("all_devices")},
			{Config: config("group", criteria("actor_group", fmt.Sprintf("group_id = %q", groupID))), Check: resource.TestCheckResourceAttr("firezone_resource.pool", "device_membership_criteria.group_id", groupID)},
			{Config: config("listed", criteria("listed", fmt.Sprintf("device_ids = [%q, %q]", groupID, deviceID))), Check: resource.TestCheckResourceAttr("firezone_resource.pool", "device_membership_criteria.device_ids.#", "2")},
			{ResourceName: "firezone_resource.pool", ImportState: true, ImportStateVerify: true},
			{Config: config("cleared", criteria("listed", "device_ids = []")), Check: resource.TestCheckResourceAttr("firezone_resource.pool", "device_membership_criteria.device_ids.#", "0")},
			{Config: config("cleared", criteria("listed", "")), Check: checkMode("listed")},
			{PreConfig: func() {
				mu.Lock()
				defer mu.Unlock()
				pool["device_membership_criteria"] = map[string]any{"device": map[string]any{"field": "id", "op": "in", "value": []any{deviceID}}}
				preserveCriteria = true
			}, Config: config("externally-managed", ""), Check: resource.TestCheckResourceAttr("firezone_resource.pool", "device_membership_criteria.device_ids.#", "1")},
			{Config: config("externally-managed", ""), PlanOnly: true},
			{PreConfig: func() { mu.Lock(); preserveCriteria = false; mu.Unlock() },
				Config: strings.Replace(config("network", `site_id = "44444444-2222-3333-4444-555555555555"
address = "10.0.0.1"`), `type = "device_pool"`, `type = "ip"`, 1),
				Check: resource.TestCheckNoResourceAttr("firezone_resource.pool", "device_membership_criteria.mode")},
			{Config: config("converted-back", ""), Check: checkMode("listed")},
		},
	})
}

func TestDeviceMembershipCriteriaRoundTrip(t *testing.T) {
	for _, raw := range []string{
		string(emptyListedCriteria),
		`{"device":{"field":"id","op":"in","value":["11111111-2222-3333-4444-555555555555"]}}`,
		`{"device":{"field":"actor_id","op":"eq","value":{"subject":"actor_id"}}}`,
		`{"device":{"field":"account_id","op":"eq","value":{"subject":"account_id"}}}`,
		`{"actor_group":{"field":"id","op":"eq","value":"11111111-2222-3333-4444-555555555555"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			model, diags := criteriaFromAPI(t.Context(), []byte(raw), types.ObjectNull(criteriaAttributeTypes))
			if diags.HasError() {
				t.Fatal(diags)
			}
			encoded, diags := criteriaToAPI(t.Context(), model)
			if diags.HasError() {
				t.Fatal(diags)
			}
			var got, want any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s, want %s", encoded, raw)
			}
		})
	}
}

func TestDeviceMembershipCriteriaValidation(t *testing.T) {
	for _, tt := range []struct {
		name, resourceType, mode string
		ids                      types.Set
		group                    types.String
		wantError                bool
	}{
		{"empty listed", "device_pool", "listed", types.SetNull(types.StringType), types.StringNull(), false},
		{"own", "device_pool", "own_devices", types.SetNull(types.StringType), types.StringNull(), false},
		{"all", "device_pool", "all_devices", types.SetNull(types.StringType), types.StringNull(), false},
		{"group", "device_pool", "actor_group", types.SetNull(types.StringType), types.StringValue("11111111-2222-3333-4444-555555555555"), false},
		{"unknown group", "device_pool", "actor_group", types.SetNull(types.StringType), types.StringUnknown(), false},
		{"missing group", "device_pool", "actor_group", types.SetNull(types.StringType), types.StringNull(), true},
		{"group on listed", "device_pool", "listed", types.SetNull(types.StringType), types.StringValue("group"), true},
		{"ids on dynamic", "device_pool", "all_devices", types.SetValueMust(types.StringType, []attr.Value{}), types.StringNull(), true},
		{"non pool", "ip", "listed", types.SetNull(types.StringType), types.StringNull(), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			value, diags := types.ObjectValueFrom(t.Context(), criteriaAttributeTypes, deviceMembershipCriteriaModel{Mode: types.StringValue(tt.mode), DeviceIDs: tt.ids, GroupID: tt.group})
			if diags.HasError() {
				t.Fatal(diags)
			}
			validateDeviceMembershipCriteria(t.Context(), resourceResourceModel{Type: types.StringValue(tt.resourceType), DeviceMembershipCriteria: value}, &diags)
			if diags.HasError() != tt.wantError {
				t.Fatalf("diagnostics %v, want error %v", diags, tt.wantError)
			}
		})
	}
}
