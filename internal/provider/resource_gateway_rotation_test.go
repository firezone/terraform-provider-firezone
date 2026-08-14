package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestRotationTriggered covers when a token_rotation_trigger change
// should rotate. Getting this wrong is expensive in both directions: a
// false negative silently skips a scheduled rotation, and a false
// positive burns a token on every apply.
func TestRotationTriggered(t *testing.T) {
	tests := []struct {
		name    string
		planned types.String
		stored  types.String
		want    bool
	}{
		{
			name:    "unchanged value does not rotate",
			planned: types.StringValue("v1"),
			stored:  types.StringValue("v1"),
			want:    false,
		},
		{
			name:    "changed value rotates",
			planned: types.StringValue("v2"),
			stored:  types.StringValue("v1"),
			want:    true,
		},
		{
			// The common case: never set, still not set. A gateway whose
			// config omits the trigger must never rotate on apply.
			name:    "null to null does not rotate",
			planned: types.StringNull(),
			stored:  types.StringNull(),
			want:    false,
		},
		{
			name:    "adding a trigger rotates",
			planned: types.StringValue("v1"),
			stored:  types.StringNull(),
			want:    true,
		},
		{
			// Removing the trigger is a config change, not a request to
			// rotate - but it does have to be treated consistently, and
			// rotating once on removal is the safer reading than
			// silently diverging from state.
			name:    "removing the trigger rotates",
			planned: types.StringNull(),
			stored:  types.StringValue("v1"),
			want:    true,
		},
		{
			// time_rotating.<name>.id is unknown until apply. It is only
			// unknown because something upstream is changing it, so this
			// must count as a rotation or scheduled rotation never fires.
			name:    "unknown planned value rotates",
			planned: types.StringUnknown(),
			stored:  types.StringValue("v1"),
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rotationTriggered(tt.planned, tt.stored); got != tt.want {
				t.Errorf("rotationTriggered(%v, %v) = %v, want %v",
					tt.planned, tt.stored, got, tt.want)
			}
		})
	}
}

// TestGatewayNameLengthValidator pins the bounds on firezone_gateway's
// name. Device.changeset/1 enforces 1-255 server-side, so without a
// plan-time validator an over-long name is a 422 during apply. The lower
// bound matters for a different reason: ProvisionGatewayRequest.Name is
// `omitempty`, so name = "" would be dropped from the request, the API
// would generate a random name, and Terraform would then report an
// inconsistent result because state disagreed with the empty config.
func TestGatewayNameLengthValidator(t *testing.T) {
	ctx := context.Background()

	schemaResp := &fwresource.SchemaResponse{}
	(&gatewayResource{}).Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %v", schemaResp.Diagnostics)
	}

	nameAttr, ok := schemaResp.Schema.Attributes["name"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("name attribute = %T, want schema.StringAttribute", schemaResp.Schema.Attributes["name"])
	}

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "typical", value: "gw-us-east-1"},
		{name: "at the maximum", value: strings.Repeat("a", 255)},
		{name: "single character", value: "a"},
		{name: "over the maximum", value: strings.Repeat("a", 256), wantErr: true},
		{name: "empty", value: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotErr bool
			for _, v := range nameAttr.Validators {
				resp := &validator.StringResponse{}
				v.ValidateString(ctx,
					validator.StringRequest{
						Path:        path.Root("name"),
						ConfigValue: types.StringValue(tt.value),
					},
					resp,
				)
				if resp.Diagnostics.HasError() {
					gotErr = true
				}
			}
			if gotErr != tt.wantErr {
				t.Errorf("validation error = %v, want %v (len %d)", gotErr, tt.wantErr, len(tt.value))
			}
		})
	}
}
