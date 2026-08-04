package provider

import (
	"context"
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestResourceResourceValidateConfig covers firezone_resource's
// conditional site_id rule, which no Required/Optional flag can express:
// site_id is required for every Resource type except static_device_pool,
// which the API detaches from any Site server-side.
//
// This drives ValidateConfig directly rather than going through
// terraform-plugin-testing, so it runs under plain `go test` with no
// terraform binary and no dev server.
func TestResourceResourceValidateConfig(t *testing.T) {
	ctx := context.Background()

	schemaResp := &fwresource.SchemaResponse{}
	(&resourceResource{}).Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %v", schemaResp.Diagnostics)
	}
	schema := schemaResp.Schema
	objType := schema.Type().TerraformType(ctx).(tftypes.Object)

	tests := []struct {
		name        string
		resType     tftypes.Value
		siteID      tftypes.Value
		ipStack     tftypes.Value
		wantErr     bool
		wantErrPart string
	}{
		{
			name:    "cidr with site_id is valid",
			resType: tftypes.NewValue(tftypes.String, "cidr"),
			siteID:  tftypes.NewValue(tftypes.String, "site-1"),
		},
		{
			name:        "cidr without site_id is rejected",
			resType:     tftypes.NewValue(tftypes.String, "cidr"),
			siteID:      tftypes.NewValue(tftypes.String, nil),
			wantErr:     true,
			wantErrPart: "site_id is required",
		},
		{
			name:    "static_device_pool without site_id is valid",
			resType: tftypes.NewValue(tftypes.String, "static_device_pool"),
			siteID:  tftypes.NewValue(tftypes.String, nil),
		},
		{
			// The important one: the API silently discards site_id here,
			// so without this check the apply succeeds and state records
			// a Site the server never stored.
			name:        "static_device_pool with site_id is rejected",
			resType:     tftypes.NewValue(tftypes.String, "static_device_pool"),
			siteID:      tftypes.NewValue(tftypes.String, "site-1"),
			wantErr:     true,
			wantErrPart: "must be omitted",
		},
		{
			// Both unknown-value cases defer to the API rather than
			// guessing, so neither may raise a plan-time error.
			name:    "unknown type defers validation",
			resType: tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			siteID:  tftypes.NewValue(tftypes.String, nil),
		},
		{
			name:    "unknown site_id defers validation",
			resType: tftypes.NewValue(tftypes.String, "static_device_pool"),
			siteID:  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		},
		{
			name:    "dns with ip_stack is valid",
			resType: tftypes.NewValue(tftypes.String, "dns"),
			siteID:  tftypes.NewValue(tftypes.String, "site-1"),
			ipStack: tftypes.NewValue(tftypes.String, "ipv4_only"),
		},
		{
			name:    "dns without ip_stack is valid",
			resType: tftypes.NewValue(tftypes.String, "dns"),
			siteID:  tftypes.NewValue(tftypes.String, "site-1"),
			ipStack: tftypes.NewValue(tftypes.String, nil),
		},
		{
			// The API rejects this with a check-constraint 422 at apply
			// time, after other resources in the same apply are created.
			name:        "ip with ip_stack is rejected",
			resType:     tftypes.NewValue(tftypes.String, "ip"),
			siteID:      tftypes.NewValue(tftypes.String, "site-1"),
			ipStack:     tftypes.NewValue(tftypes.String, "ipv4_only"),
			wantErr:     true,
			wantErrPart: "ip_stack applies only to",
		},
		{
			name:        "static_device_pool with ip_stack is rejected",
			resType:     tftypes.NewValue(tftypes.String, "static_device_pool"),
			siteID:      tftypes.NewValue(tftypes.String, nil),
			ipStack:     tftypes.NewValue(tftypes.String, "dual"),
			wantErr:     true,
			wantErrPart: "ip_stack applies only to",
		},
		{
			name:    "unknown ip_stack defers validation",
			resType: tftypes.NewValue(tftypes.String, "ip"),
			siteID:  tftypes.NewValue(tftypes.String, "site-1"),
			ipStack: tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipStack := tt.ipStack
			if ipStack.IsNull() && ipStack.Type() == nil {
				ipStack = tftypes.NewValue(tftypes.String, nil)
			}

			raw := tftypes.NewValue(objType, map[string]tftypes.Value{
				"id":                  tftypes.NewValue(tftypes.String, nil),
				"site_id":             tt.siteID,
				"name":                tftypes.NewValue(tftypes.String, "res"),
				"type":                tt.resType,
				"address":             tftypes.NewValue(tftypes.String, nil),
				"address_description": tftypes.NewValue(tftypes.String, nil),
				"ip_stack":            ipStack,
				"filters":             tftypes.NewValue(objType.AttributeTypes["filters"], nil),
			})

			resp := &fwresource.ValidateConfigResponse{}
			(&resourceResource{}).ValidateConfig(ctx,
				fwresource.ValidateConfigRequest{
					Config: tfsdk.Config{Raw: raw, Schema: schema},
				},
				resp,
			)

			gotErr := resp.Diagnostics.HasError()
			if gotErr != tt.wantErr {
				t.Fatalf("HasError() = %v, want %v (diags: %v)", gotErr, tt.wantErr, resp.Diagnostics)
			}
			if !tt.wantErr {
				return
			}

			var found bool
			for _, d := range resp.Diagnostics.Errors() {
				if strings.Contains(d.Detail(), tt.wantErrPart) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("diagnostics = %v, want one mentioning %q", resp.Diagnostics, tt.wantErrPart)
			}
		})
	}
}
