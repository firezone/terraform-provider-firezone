package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestPolicyResourceValidateConfig covers the property/operator pairing
// rule. Each attribute has its own OneOf validator, but those check
// values in isolation - the pairing is what makes a condition
// meaningful, and only ValidateConfig can see both at once.
//
// Runs under plain `go test`: no terraform binary, no dev server.
func TestPolicyResourceValidateConfig(t *testing.T) {
	ctx := context.Background()

	schemaResp := &fwresource.SchemaResponse{}
	(&policyResource{}).Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %v", schemaResp.Diagnostics)
	}
	schema := schemaResp.Schema
	objType := schema.Type().TerraformType(ctx).(tftypes.Object)
	conditionListType := objType.AttributeTypes["condition"].(tftypes.List)
	conditionType := conditionListType.ElementType.(tftypes.Object)

	condition := func(property, operator tftypes.Value) tftypes.Value {
		return tftypes.NewValue(conditionType, map[string]tftypes.Value{
			"property": property,
			"operator": operator,
			"values": tftypes.NewValue(
				tftypes.List{ElementType: tftypes.String},
				[]tftypes.Value{tftypes.NewValue(tftypes.String, "x")},
			),
		})
	}
	str := func(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }
	unknown := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)

	tests := []struct {
		name        string
		conditions  []tftypes.Value
		wantErr     bool
		wantErrPart string
		wantPath    string
	}{
		// Every pair the API accepts, per
		// Portal.Policies.Condition.valid_operators_for_property/1.
		{
			name:       "region is_in",
			conditions: []tftypes.Value{condition(str("remote_ip_location_region"), str("is_in"))},
		},
		{
			name:       "region is_not_in",
			conditions: []tftypes.Value{condition(str("remote_ip_location_region"), str("is_not_in"))},
		},
		{
			name:       "remote_ip is_in_cidr",
			conditions: []tftypes.Value{condition(str("remote_ip"), str("is_in_cidr"))},
		},
		{
			name:       "remote_ip is_not_in_cidr",
			conditions: []tftypes.Value{condition(str("remote_ip"), str("is_not_in_cidr"))},
		},
		{
			name:       "auth_provider_id is_in",
			conditions: []tftypes.Value{condition(str("auth_provider_id"), str("is_in"))},
		},
		{
			name:       "current_utc_datetime day ranges",
			conditions: []tftypes.Value{condition(str("current_utc_datetime"), str("is_in_day_of_week_time_ranges"))},
		},
		{
			name:       "client_verified is",
			conditions: []tftypes.Value{condition(str("client_verified"), str("is"))},
		},

		// Mismatches: each operator is individually valid, so only the
		// pairing check catches these.
		{
			name:        "current_utc_datetime with is_in",
			conditions:  []tftypes.Value{condition(str("current_utc_datetime"), str("is_in"))},
			wantErr:     true,
			wantErrPart: "is_in_day_of_week_time_ranges",
		},
		{
			name:        "remote_ip with is_in",
			conditions:  []tftypes.Value{condition(str("remote_ip"), str("is_in"))},
			wantErr:     true,
			wantErrPart: "is_in_cidr, is_not_in_cidr",
		},
		{
			name:        "client_verified with is_in",
			conditions:  []tftypes.Value{condition(str("client_verified"), str("is_in"))},
			wantErr:     true,
			wantErrPart: `operator "is_in" does not apply`,
		},
		{
			name:        "region with is_in_cidr",
			conditions:  []tftypes.Value{condition(str("remote_ip_location_region"), str("is_in_cidr"))},
			wantErr:     true,
			wantErrPart: "is_in, is_not_in",
		},

		{
			// The bad block is the second one, so this pins that the
			// error is attributed to the right list index rather than
			// to the block the practitioner got right.
			name: "valid block followed by invalid one",
			conditions: []tftypes.Value{
				condition(str("remote_ip_location_region"), str("is_in")),
				condition(str("client_verified"), str("is_in")),
			},
			wantErr:     true,
			wantErrPart: "does not apply",
			wantPath:    "condition[1].operator",
		},

		// Unknown values defer to the API rather than guessing.
		{
			name:       "unknown property defers",
			conditions: []tftypes.Value{condition(unknown, str("is_in"))},
		},
		{
			name:       "unknown operator defers",
			conditions: []tftypes.Value{condition(str("current_utc_datetime"), unknown)},
		},

		{
			name:       "no conditions",
			conditions: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions := tftypes.NewValue(conditionListType, tt.conditions)
			if tt.conditions == nil {
				conditions = tftypes.NewValue(conditionListType, nil)
			}

			raw := tftypes.NewValue(objType, map[string]tftypes.Value{
				"id":                       tftypes.NewValue(tftypes.String, nil),
				"group_id":                 str("group-1"),
				"resource_id":              str("res-1"),
				"description":              tftypes.NewValue(tftypes.String, nil),
				"flow_log_uploads_enabled": tftypes.NewValue(tftypes.Bool, nil),
				"condition":                conditions,
			})

			resp := &fwresource.ValidateConfigResponse{}
			(&policyResource{}).ValidateConfig(ctx,
				fwresource.ValidateConfigRequest{
					Config: tfsdk.Config{Raw: raw, Schema: schema},
				},
				resp,
			)

			if got := resp.Diagnostics.HasError(); got != tt.wantErr {
				t.Fatalf("HasError() = %v, want %v (diags: %v)", got, tt.wantErr, resp.Diagnostics)
			}
			if !tt.wantErr {
				return
			}

			var found bool
			for _, d := range resp.Diagnostics.Errors() {
				if !strings.Contains(d.Detail(), tt.wantErrPart) {
					continue
				}
				found = true

				if tt.wantPath == "" {
					break
				}
				withPath, ok := d.(diag.DiagnosticWithPath)
				if !ok {
					t.Fatalf("diagnostic %v carries no path, want %q", d, tt.wantPath)
				}
				if got := withPath.Path().String(); got != tt.wantPath {
					t.Errorf("diagnostic path = %q, want %q", got, tt.wantPath)
				}
				break
			}
			if !found {
				t.Errorf("diagnostics = %v, want one mentioning %q", resp.Diagnostics, tt.wantErrPart)
			}
		})
	}
}

// TestValidOperatorsForProperty_CoversEveryProperty guards the map
// against drifting from the schema's own enum: a property accepted by
// the OneOf validator but absent from the map would silently skip
// pairing validation entirely.
func TestValidOperatorsForProperty_CoversEveryProperty(t *testing.T) {
	properties := []string{
		"remote_ip_location_region",
		"remote_ip",
		"auth_provider_id",
		"current_utc_datetime",
		"client_verified",
	}

	for _, property := range properties {
		if _, ok := validOperatorsForProperty[property]; !ok {
			t.Errorf("validOperatorsForProperty is missing %q", property)
		}
	}
	if len(validOperatorsForProperty) != len(properties) {
		t.Errorf("validOperatorsForProperty has %d entries, want %d - a property was added "+
			"to the map without updating this test, or vice versa",
			len(validOperatorsForProperty), len(properties))
	}
}
