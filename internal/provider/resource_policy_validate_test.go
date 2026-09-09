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
		// current_utc_datetime values carry a format ValidateConfig
		// checks; every other property's are opaque to it. Keep this
		// helper's values valid so the pairing assertions below fail
		// only on the pairing.
		value := "x"
		if property.IsKnown() && !property.IsNull() {
			var s string
			if err := property.As(&s); err == nil && s == propertyCurrentUTCDatetime {
				value = "M/09:00-17:00/America/New_York"
			}
		}

		return tftypes.NewValue(conditionType, map[string]tftypes.Value{
			"property": property,
			"operator": operator,
			"values": tftypes.NewValue(
				tftypes.List{ElementType: tftypes.String},
				[]tftypes.Value{tftypes.NewValue(tftypes.String, value)},
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

		// The API allows at most one condition per property.
		{
			name: "duplicate property",
			conditions: []tftypes.Value{
				condition(str("remote_ip_location_region"), str("is_in")),
				condition(str("remote_ip_location_region"), str("is_not_in")),
			},
			wantErr:     true,
			wantErrPart: "already constrained by the condition at index 0",
			wantPath:    "condition[1].property",
		},
		{
			name: "distinct properties are not duplicates",
			conditions: []tftypes.Value{
				condition(str("remote_ip_location_region"), str("is_in")),
				condition(str("remote_ip"), str("is_in_cidr")),
			},
		},
		{
			// The unknown block can't be compared, so the two known
			// ones must still be caught around it.
			name: "duplicate property either side of an unknown one",
			conditions: []tftypes.Value{
				condition(str("client_verified"), str("is")),
				condition(unknown, str("is_in")),
				condition(str("client_verified"), str("is")),
			},
			wantErr:     true,
			wantErrPart: "already constrained by the condition at index 0",
			wantPath:    "condition[2].property",
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

// TestPolicyResourceValidateConfig_DayTimeRangeValues covers the
// "DAY/TIME_RANGES/TIMEZONE" format carried by current_utc_datetime
// values. The API rejects a malformed value with a bare 422 naming no
// field, so a Policy with one value per weekday gives no clue which is
// wrong.
func TestPolicyResourceValidateConfig_DayTimeRangeValues(t *testing.T) {
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

	tests := []struct {
		name        string
		values      []tftypes.Value
		wantErr     bool
		wantErrPart string
		wantPath    string
	}{
		{
			name:   "valid single range",
			values: []tftypes.Value{tftypes.NewValue(tftypes.String, "M/09:00-17:00/America/New_York")},
		},
		{
			name:   "valid multiple ranges",
			values: []tftypes.Value{tftypes.NewValue(tftypes.String, "R/09:00-12:00,13:00-17:00/UTC")},
		},
		{
			name: "valid value per weekday",
			values: []tftypes.Value{
				tftypes.NewValue(tftypes.String, "M/09:00-17:00/Europe/Berlin"),
				tftypes.NewValue(tftypes.String, "T/09:00-17:00/Europe/Berlin"),
				tftypes.NewValue(tftypes.String, "W/09:00-17:00/Europe/Berlin"),
				tftypes.NewValue(tftypes.String, "R/09:00-17:00/Europe/Berlin"),
				tftypes.NewValue(tftypes.String, "F/09:00-17:00/Europe/Berlin"),
			},
		},
		{
			name:   "midnight boundary",
			values: []tftypes.Value{tftypes.NewValue(tftypes.String, "S/00:00-23:59/UTC")},
		},

		// Every malformed shape reproduced against the API, each of
		// which planned clean and 422'd at apply.
		{
			name:        "day letter not recognised",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "X/09:00-17:00/America/New_York")},
			wantErr:     true,
			wantErrPart: `day "X" is not one of`,
		},
		{
			name:        "timezone segment missing",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "M/09:00-17:00")},
			wantErr:     true,
			wantErrPart: "expected 3 \"/\"-separated segments, got 2",
		},
		{
			name:        "hours out of range",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "M/25:00-99:00/America/New_York")},
			wantErr:     true,
			wantErrPart: `hour "25" is not in 00-23`,
		},
		{
			name:        "not an IANA timezone",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "M/09:00-17:00/Not/A/Timezone")},
			wantErr:     true,
			wantErrPart: `timezone "Not/A/Timezone" is not an IANA timezone name`,
		},
		{
			name:        "range ends before it starts",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "M/17:00-09:00/America/New_York")},
			wantErr:     true,
			wantErrPart: "ends at or before it starts",
		},
		{
			name:        "empty range",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "M/09:00-09:00/UTC")},
			wantErr:     true,
			wantErrPart: "ends at or before it starts",
		},
		{
			name:        "minutes out of range",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "M/09:60-17:00/UTC")},
			wantErr:     true,
			wantErrPart: `minute "60" is not in 00-59`,
		},
		{
			name:        "single-digit hour",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "M/9:00-17:00/UTC")},
			wantErr:     true,
			wantErrPart: "is not \"HH:MM\"",
		},
		{
			name:        "range missing its separator",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "M/09:0017:00/UTC")},
			wantErr:     true,
			wantErrPart: "is not \"HH:MM-HH:MM\"",
		},
		{
			name:        "multi-letter day",
			values:      []tftypes.Value{tftypes.NewValue(tftypes.String, "MO/09:00-17:00/UTC")},
			wantErr:     true,
			wantErrPart: `day "MO" is not one of`,
		},
		{
			// The point of the whole check: name which of the five is
			// wrong, which the API's bare 422 never does.
			name: "third value of five is malformed",
			values: []tftypes.Value{
				tftypes.NewValue(tftypes.String, "M/09:00-17:00/UTC"),
				tftypes.NewValue(tftypes.String, "T/09:00-17:00/UTC"),
				tftypes.NewValue(tftypes.String, "W/09:00-17:00/Mars/Olympus"),
				tftypes.NewValue(tftypes.String, "R/09:00-17:00/UTC"),
				tftypes.NewValue(tftypes.String, "F/09:00-17:00/UTC"),
			},
			wantErr:     true,
			wantErrPart: `timezone "Mars/Olympus" is not an IANA timezone name`,
			wantPath:    "condition[0].values[2]",
		},

		// Unknown values defer to the API rather than guessing.
		{
			name:   "unknown element defers",
			values: []tftypes.Value{tftypes.NewValue(tftypes.String, tftypes.UnknownValue)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tftypes.NewValue(objType, map[string]tftypes.Value{
				"id":                       tftypes.NewValue(tftypes.String, nil),
				"group_id":                 tftypes.NewValue(tftypes.String, "group-1"),
				"resource_id":              tftypes.NewValue(tftypes.String, "res-1"),
				"description":              tftypes.NewValue(tftypes.String, nil),
				"flow_log_uploads_enabled": tftypes.NewValue(tftypes.Bool, nil),
				"condition": tftypes.NewValue(conditionListType, []tftypes.Value{
					tftypes.NewValue(conditionType, map[string]tftypes.Value{
						"property": tftypes.NewValue(tftypes.String, propertyCurrentUTCDatetime),
						"operator": tftypes.NewValue(tftypes.String, "is_in_day_of_week_time_ranges"),
						"values": tftypes.NewValue(
							tftypes.List{ElementType: tftypes.String},
							tt.values,
						),
					}),
				}),
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
