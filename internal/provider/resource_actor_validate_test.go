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

// TestActorResourceValidateConfig covers email's dependence on type.
// Neither attribute's own validator can see the other, so only
// ValidateConfig catches the two combinations the API rejects - and it
// rejects both with "(type: is invalid)", blaming the attribute the
// practitioner got right.
//
// Runs under plain `go test`: no terraform binary, no dev server.
func TestActorResourceValidateConfig(t *testing.T) {
	ctx := context.Background()

	schemaResp := &fwresource.SchemaResponse{}
	(&actorResource{}).Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %v", schemaResp.Diagnostics)
	}
	schema := schemaResp.Schema
	objType := schema.Type().TerraformType(ctx).(tftypes.Object)

	str := func(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }
	null := tftypes.NewValue(tftypes.String, nil)
	unknown := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)

	tests := []struct {
		name        string
		actorType   tftypes.Value
		email       tftypes.Value
		wantErr     bool
		wantErrPart string
	}{
		{
			name:      "user with email",
			actorType: str(actorTypeUser),
			email:     str("user@example.com"),
		},
		{
			name:      "admin user with email",
			actorType: str(actorTypeAdminUser),
			email:     str("admin@example.com"),
		},
		{
			name:      "service account without email",
			actorType: str(actorTypeServiceAccount),
			email:     null,
		},

		{
			name:        "service account with email",
			actorType:   str(actorTypeServiceAccount),
			email:       str("svc@example.com"),
			wantErr:     true,
			wantErrPart: "email must be omitted when type is \"service_account\"",
		},
		{
			name:        "user without email",
			actorType:   str(actorTypeUser),
			email:       null,
			wantErr:     true,
			wantErrPart: "email is required when type is \"account_user\"",
		},
		{
			name:        "admin user without email",
			actorType:   str(actorTypeAdminUser),
			email:       null,
			wantErr:     true,
			wantErrPart: "email is required when type is \"account_admin_user\"",
		},

		// Either value can come from an expression unresolved until
		// apply; the API still enforces the rule.
		{
			name:      "unknown type defers",
			actorType: unknown,
			email:     null,
		},
		{
			name:      "unknown email defers",
			actorType: str(actorTypeServiceAccount),
			email:     unknown,
		},
		{
			// type is Required, so a null one is already reported by
			// the framework - there is no rule to apply on top of it.
			name:      "null type defers",
			actorType: null,
			email:     null,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tftypes.NewValue(objType, map[string]tftypes.Value{
				"id":                      null,
				"name":                    str("actor-1"),
				"email":                   tt.email,
				"type":                    tt.actorType,
				"allow_email_otp_sign_in": tftypes.NewValue(tftypes.Bool, nil),
				"enabled":                 tftypes.NewValue(tftypes.Bool, nil),
			})

			resp := &fwresource.ValidateConfigResponse{}
			(&actorResource{}).ValidateConfig(ctx,
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

				// The whole point is attributing the error to email
				// rather than to type, as the API does.
				withPath, ok := d.(diag.DiagnosticWithPath)
				if !ok {
					t.Fatalf("diagnostic %v carries no path, want email", d)
				}
				if got := withPath.Path().String(); got != "email" {
					t.Errorf("diagnostic path = %q, want %q", got, "email")
				}
				break
			}
			if !found {
				t.Errorf("diagnostics = %v, want one mentioning %q", resp.Diagnostics, tt.wantErrPart)
			}
		})
	}
}
