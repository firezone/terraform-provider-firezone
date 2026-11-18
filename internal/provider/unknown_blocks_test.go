package provider

import (
	"context"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A dynamic block inside a for_each resource expands to an unknown list
// during validation and planning. The handlers that read the config or
// plan must accept that instead of failing with a Value Conversion Error.
// See https://github.com/firezone/terraform-provider-firezone/issues/29.

func TestResourceResource_UnknownFilters(t *testing.T) {
	ctx := context.Background()

	schemaResp := &fwresource.SchemaResponse{}
	(&resourceResource{}).Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema
	objType := schema.Type().TerraformType(ctx).(tftypes.Object)

	attrs := func(ipStack tftypes.Value) tftypes.Value {
		return tftypes.NewValue(objType, map[string]tftypes.Value{
			"id":                         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"site_id":                    tftypes.NewValue(tftypes.String, "site-1"),
			"name":                       tftypes.NewValue(tftypes.String, "res"),
			"type":                       tftypes.NewValue(tftypes.String, "ip"),
			"address":                    tftypes.NewValue(tftypes.String, "10.0.0.1"),
			"address_description":        tftypes.NewValue(tftypes.String, nil),
			"ip_stack":                   ipStack,
			"device_membership_criteria": tftypes.NewValue(objType.AttributeTypes["device_membership_criteria"], nil),
			"filters":                    tftypes.NewValue(objType.AttributeTypes["filters"], tftypes.UnknownValue),
		})
	}
	raw := attrs(tftypes.NewValue(tftypes.String, nil))

	t.Run("ValidateConfig", func(t *testing.T) {
		resp := &fwresource.ValidateConfigResponse{}
		(&resourceResource{}).ValidateConfig(ctx,
			fwresource.ValidateConfigRequest{Config: tfsdk.Config{Raw: raw, Schema: schema}},
			resp,
		)
		if resp.Diagnostics.HasError() {
			t.Fatalf("ValidateConfig returned diagnostics: %v", resp.Diagnostics)
		}
	})

	t.Run("ModifyPlan", func(t *testing.T) {
		// ip_stack is Optional+Computed, so with no config value the
		// plan holds it as unknown until ModifyPlan resolves it.
		plan := tfsdk.Plan{Raw: attrs(tftypes.NewValue(tftypes.String, tftypes.UnknownValue)), Schema: schema}
		resp := &fwresource.ModifyPlanResponse{Plan: plan}
		(&resourceResource{}).ModifyPlan(ctx,
			fwresource.ModifyPlanRequest{
				Config: tfsdk.Config{Raw: raw, Schema: schema},
				Plan:   plan,
				State:  tfsdk.State{Raw: tftypes.NewValue(objType, nil), Schema: schema},
			},
			resp,
		)
		if resp.Diagnostics.HasError() {
			t.Fatalf("ModifyPlan returned diagnostics: %v", resp.Diagnostics)
		}
		// ip_stack only applies to dns Resources, so ModifyPlan must
		// still run its rules and clear it here.
		m := map[string]tftypes.Value{}
		if err := resp.Plan.Raw.As(&m); err != nil {
			t.Fatal(err)
		}
		if ipStack := m["ip_stack"]; !ipStack.IsNull() {
			t.Errorf("planned ip_stack = %v, want null", ipStack)
		}
	})
}

func TestPolicyResource_UnknownCondition(t *testing.T) {
	ctx := context.Background()

	schemaResp := &fwresource.SchemaResponse{}
	(&policyResource{}).Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema
	objType := schema.Type().TerraformType(ctx).(tftypes.Object)

	raw := tftypes.NewValue(objType, map[string]tftypes.Value{
		"id":                       tftypes.NewValue(tftypes.String, nil),
		"group_id":                 tftypes.NewValue(tftypes.String, "group-1"),
		"resource_id":              tftypes.NewValue(tftypes.String, "res-1"),
		"description":              tftypes.NewValue(tftypes.String, nil),
		"flow_log_uploads_enabled": tftypes.NewValue(tftypes.Bool, nil),
		"require_attestation":      tftypes.NewValue(tftypes.Bool, nil),
		"enabled":                  tftypes.NewValue(tftypes.Bool, nil),
		"condition":                tftypes.NewValue(objType.AttributeTypes["condition"], tftypes.UnknownValue),
		"posture":                  tftypes.NewValue(objType.AttributeTypes["posture"], nil),
	})

	resp := &fwresource.ValidateConfigResponse{}
	(&policyResource{}).ValidateConfig(ctx,
		fwresource.ValidateConfigRequest{Config: tfsdk.Config{Raw: raw, Schema: schema}},
		resp,
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("ValidateConfig returned diagnostics: %v", resp.Diagnostics)
	}
}
