package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-go"
)

var (
	_ resource.Resource                = &gatewayResource{}
	_ resource.ResourceWithImportState = &gatewayResource{}
	_ resource.ResourceWithConfigure   = &gatewayResource{}
)

// NewGatewayResource returns a new firezone_gateway resource instance,
// for use with FirezoneProvider.Resources.
//
// CRITICAL, read before touching this file: the API mints the Gateway's
// token once, on POST, and never re-exposes it - GET responses have no
// token field at all (see firezone.Gateway vs firezone.ProvisionedGateway
// in api-client). Read must leave the existing "token" state value
// untouched; it does NOT overwrite it with an empty value, because
// there's nothing to overwrite it with. This means:
//
//  1. Out-of-band token rotation leaves Terraform state silently
//     holding a stale token, with no way for Read to detect the drift.
//  2. terraform import can never populate "token" - importing only
//     adopts a Gateway into state for rename/delete lifecycle
//     management, not credential retrieval.
//
// This is a fundamental limitation of the underlying single-owner-token
// API design, not a bug in Read. Do not "fix" Read to clear or attempt
// to refresh token.
func NewGatewayResource() resource.Resource {
	return &gatewayResource{}
}

type gatewayResource struct {
	client *firezone.Client
}

// gatewayResourceModel mirrors the firezone_gateway resource schema.
type gatewayResourceModel struct {
	ID     types.String `tfsdk:"id"`
	SiteID types.String `tfsdk:"site_id"`
	Name   types.String `tfsdk:"name"`
	Token  types.String `tfsdk:"token"`
}

func (r *gatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway"
}

func (r *gatewayResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Provisions a Gateway and mints its single-owner token in one call.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Gateway ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"site_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the Site this Gateway belongs to. Immutable - a Gateway cannot move Sites.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Gateway name. Randomly generated when omitted.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"token": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "One-time Gateway token secret, returned only when this resource is created. " +
					"The API never re-exposes it: out-of-band token rotation leaves this value stale with no " +
					"drift detection possible, and `terraform import` cannot populate it at all.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *gatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	r.client = client
}

func (r *gatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan gatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	siteID := plan.SiteID.ValueString()
	provisioned, err := r.client.Sites.Gateways(siteID).Provision(ctx, &firezone.ProvisionGatewayRequest{
		Name: plan.Name.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Provisioning Gateway", err.Error())
		return
	}

	plan.ID = types.StringValue(provisioned.ID)
	plan.Name = types.StringValue(provisioned.Name)
	plan.Token = types.StringValue(provisioned.Token)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state gatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, err := r.client.Sites.Gateways(state.SiteID.ValueString()).Get(ctx, state.ID.ValueString())
	if err != nil {
		if firezone.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error Reading Gateway", err.Error())
		return
	}

	// Deliberately does not touch state.Token - see the package-level
	// comment on NewGatewayResource.
	state.Name = types.StringValue(found.Name)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan gatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state gatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Update only ever changes name (site_id is RequiresReplace); carry
	// the existing token through unconditionally rather than relying on
	// the plan to have it (it won't - see NewGatewayResource).
	plan.Token = state.Token

	updated, err := r.client.Sites.Gateways(plan.SiteID.ValueString()).Update(ctx, plan.ID.ValueString(), &firezone.UpdateGatewayRequest{
		Name: plan.Name.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Updating Gateway", err.Error())
		return
	}

	plan.Name = types.StringValue(updated.Name)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Sites.Gateways(state.SiteID.ValueString()).Delete(ctx, state.ID.ValueString())
	if err != nil && !firezone.IsNotFound(err) {
		resp.Diagnostics.AddError("Error Deleting Gateway", err.Error())
	}
}

// ImportState adopts an existing Gateway into Terraform state for
// rename/delete lifecycle management. "token" is left empty - the API
// gives no way to recover it after creation.
func (r *gatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	siteID, gatewayID, err := parseGatewayImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, siteIDPath, siteID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, idPath, gatewayID)...)
	resp.Diagnostics.AddWarning(
		"Imported Gateway Has No Token in State",
		"The API cannot return an existing Gateway's token, so \"token\" will be empty after this import. "+
			"terraform plan will show token changing from null to (known after apply) on the next apply "+
			"unless you rotate the token out-of-band first - importing a Gateway does not make its running "+
			"configuration invalid.",
	)
}

func parseGatewayImportID(id string) (siteID, gatewayID string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected import ID in the form \"site_id/gateway_id\", got: %q", id)
	}
	return parts[0], parts[1], nil
}
