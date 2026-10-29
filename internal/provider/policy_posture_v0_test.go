package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// upgradePolicyState runs a raw firezone_policy state at version 0
// through the provider's UpgradeResourceState and returns the upgraded
// posture value.
func upgradePolicyState(t *testing.T, rawState string) postureValue {
	t.Helper()
	ctx := context.Background()

	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	// The framework needs the schemas loaded before it serves requests.
	if _, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{}); err != nil {
		t.Fatal(err)
	}
	resp, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "firezone_policy",
		Version:  0,
		RawState: &tfprotov6.RawState{JSON: []byte(rawState)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("UpgradeResourceState: %s: %s", d.Summary, d.Detail)
		}
	}

	var schemaResp fwresource.SchemaResponse
	(&policyResource{}).Schema(ctx, fwresource.SchemaRequest{}, &schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx)
	state, err := resp.UpgradedState.Unmarshal(objType)
	if err != nil {
		t.Fatal(err)
	}
	var attrs map[string]tftypes.Value
	if err := state.As(&attrs); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := attrs["id"].As(&id); err != nil || id != "pol-1" {
		t.Errorf("id = %q (%v), want it carried over", id, err)
	}
	posture, err := postureType{}.ValueFromTerraform(ctx, attrs["posture"])
	if err != nil {
		t.Fatal(err)
	}
	return posture.(postureValue)
}

const policyStateV0Prefix = `{
  "id": "pol-1",
  "group_id": "g-1",
  "resource_id": "r-1",
  "description": null,
  "flow_log_uploads_enabled": true,
  "enabled": true,
  "require_attestation": false,
  "condition": [],
`

func TestUpgradePolicyStateV0(t *testing.T) {
	got := upgradePolicyState(t, policyStateV0Prefix+`
  "posture": [{
    "operator": "and",
    "negate": false,
    "check": [
      {"field": "intune.enrolled", "operator": "is", "value": "true", "values": null, "rows": "any"},
      {"field": "defender.os_build", "operator": "gte", "value": "40", "values": null, "rows": "any"},
      {"field": "iru.device_capacity_gb", "operator": "gte", "value": "0.5", "values": null, "rows": "any"},
      {"field": "iru.tags", "operator": "exists", "value": null, "values": null, "rows": "any"}
    ],
    "group": [{
      "operator": "or",
      "negate": true,
      "check": [
        {"field": "intune.compliance_state", "operator": "is_in", "value": null, "values": ["compliant", "inGracePeriod"], "rows": "all"},
        {"field": "firezone.last_seen_version", "operator": "gte", "value": "@latest", "values": null, "rows": "any"}
      ],
      "group": []
    }]
  }]
}`)

	node, diags := postureFromValue(got)
	if diags.HasError() || node == nil {
		t.Fatalf("upgraded posture does not parse: %v", diags)
	}
	wire, _ := json.Marshal(node)
	want := `{"and":[` +
		`{"field":"intune.enrolled","op":"is","value":true},` +
		`{"field":"defender.os_build","op":"gte","value":40},` +
		`{"field":"iru.device_capacity_gb","op":"gte","value":0.5},` +
		`{"field":"iru.tags","op":"exists"},` +
		`{"not":{"or":[{"field":"intune.compliance_state","op":"is_in","rows":"all","value":["compliant","inGracePeriod"]},` +
		`{"field":"firezone.last_seen_version","op":"gte","value":"@latest"}]}}]}`
	if string(wire) != want {
		t.Errorf("upgraded posture\n got: %s\nwant: %s", wire, want)
	}

	// It is written in the spelling Read uses, so the first refresh after
	// the upgrade changes nothing.
	canonical, err := postureToValue(node)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(canonical) {
		t.Errorf("upgraded posture is not in Read's spelling\n got: %v\nwant: %v", got, canonical)
	}
}

func TestUpgradePolicyStateV0WithoutPosture(t *testing.T) {
	for name, posture := range map[string]string{"empty": `[]`, "null": `null`} {
		got := upgradePolicyState(t, policyStateV0Prefix+`"posture": `+posture+`}`)
		if !got.IsNull() {
			t.Errorf("%s: want a null posture, got %v", name, got)
		}
	}
}

// TestUpgradePolicyStateV0From02 upgrades state written by 0.2.x, which
// had posture blocks but no require_attestation.
func TestUpgradePolicyStateV0From02(t *testing.T) {
	got := upgradePolicyState(t, `{
  "id": "pol-1",
  "group_id": "g-1",
  "resource_id": "r-1",
  "description": null,
  "flow_log_uploads_enabled": true,
  "enabled": true,
  "condition": [],
  "posture": [{
    "operator": "or",
    "negate": false,
    "check": [{"field": "intune.enrolled", "operator": "is", "value": "true", "values": null, "rows": "any"}],
    "group": []
  }]
}`)
	node, diags := postureFromValue(got)
	if diags.HasError() || node == nil || len(node.Or) != 1 || node.Or[0].Value != true {
		t.Fatalf("want or(intune.enrolled is true), got %+v (%v)", node, diags)
	}
}
