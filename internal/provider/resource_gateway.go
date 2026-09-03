package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-go"
)

var (
	_ resource.Resource                = &gatewayResource{}
	_ resource.ResourceWithImportState = &gatewayResource{}
	_ resource.ResourceWithConfigure   = &gatewayResource{}
	_ resource.ResourceWithModifyPlan  = &gatewayResource{}
)

// NewGatewayResource returns a new firezone_gateway resource instance,
// for use with FirezoneProvider.Resources.
//
// CRITICAL, read before touching this file: the API mints the Gateway's
// token once, on POST, and never re-exposes it - GET responses have no
// token field at all (see firezone.Gateway vs firezone.ProvisionedGateway
// in firezone-go). Read must leave the existing "token" state value
// untouched; it does NOT overwrite it with an empty value, because
// there's nothing to overwrite it with. This means:
//
//  1. Out-of-band token rotation leaves Terraform state holding a stale
//     token. Read cannot recover the replacement - the API returns it
//     once - but it can now detect the situation via the Gateway's
//     rotated_at and warn, which is why Read emits a diagnostic rather
//     than silently carrying on. Rotating through
//     token_rotation_trigger is what puts the new secret in state.
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
	ID                   types.String `tfsdk:"id"`
	SiteID               types.String `tfsdk:"site_id"`
	Name                 types.String `tfsdk:"name"`
	Token                types.String `tfsdk:"token"`
	TokenRotationTrigger types.String `tfsdk:"token_rotation_trigger"`
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
				Optional: true,
				Computed: true,
				Description: "Gateway name, 1-255 characters. Randomly generated when omitted - " +
					"omit the attribute entirely for that, rather than setting it to an empty " +
					"string.",
				Validators: []validator.String{
					// Device.changeset/1 bounds the name at 1-255 and trims
					// first, so without this an over-long name is a 422 at
					// apply time, after other resources in the same apply
					// have been created.
					//
					// The lower bound also closes a trap of our own making:
					// ProvisionGatewayRequest.Name is `omitempty`, so name =
					// "" is dropped from the request, the API generates a
					// random name, and Terraform then reports an
					// inconsistent result because state disagrees with the
					// empty config value.
					stringvalidator.LengthBetween(1, 255),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"token": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "Gateway token secret. Returned when this resource is created, and again " +
					"each time token_rotation_trigger changes. The API never re-exposes it otherwise, so " +
					"`terraform import` cannot populate it and a rotation performed outside Terraform " +
					"leaves this value stale.",
				PlanModifiers: []planmodifier.String{
					// Keep the stored secret across plans, except when the
					// trigger changes - rotateOnTriggerChange marks it
					// unknown there, so the plan shows the token being
					// replaced and downstream consumers re-read it.
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"token_rotation_trigger": schema.StringAttribute{
				Optional: true,
				Description: "Rotates the Gateway's token whenever this value changes, replacing " +
					"`token` with the new secret. The value itself is arbitrary and is never sent to " +
					"the API - pair it with time_rotating.<name>.id for scheduled rotation, or set it " +
					"to any string you bump by hand.\n\n" +
					"Rotation is not instant. The old token keeps working until the Gateway connects " +
					"with the replacement or the API's grace period elapses, whichever comes first - " +
					"so whatever configures the Gateway host must pick up the new `token` and restart " +
					"within that window, or the Gateway is stranded. Once pickup is confirmed the old " +
					"token is deleted, so rolling back to it will not work.",
			},
		},
	}
}

// ModifyPlan marks token as unknown when token_rotation_trigger
// changes, so the plan shows the secret being replaced rather than
// carried forward by UseStateForUnknown. Without this the plan claims
// token is unchanged and any downstream consumer - a secret manager
// entry, a host's user_data - would never see the new value.
//
// It also warns when a rotation is already pending, because rotating
// again replaces only the pending token: the Gateway keeps running on
// the in-use one, whose deadline does not reset.
func (r *gatewayResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Nothing to do on create (no state) or destroy (no plan).
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state gatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !rotationTriggered(plan.TokenRotationTrigger, state.TokenRotationTrigger) {
		return
	}

	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("token"), types.StringUnknown())...)

	if r.client == nil {
		return
	}

	// Best-effort: a lookup failure here must not block the plan, since
	// the warning is advisory and Update would surface a real error.
	found, err := r.client.Sites.Gateways(state.SiteID.ValueString()).
		Get(ctx, state.ID.ValueString())
	if err != nil || !found.RotationPending() {
		return
	}

	resp.Diagnostics.AddWarning(
		"Gateway Token Rotation Already Pending",
		fmt.Sprintf("This Gateway has an unconfirmed token rotation from %s - it has not yet "+
			"connected with its replacement. Rotating again replaces only that pending token; "+
			"the token the Gateway is actually running keeps its original deadline and is not "+
			"extended. Confirm the Gateway picked up the previous replacement before rotating "+
			"again.", found.RotatedAt.Format(time.RFC3339)),
	)
}

// rotationTriggered reports whether the trigger changed in a way that
// should rotate. An unknown planned value counts: it is only unknown
// because something upstream will change it.
func rotationTriggered(planned, stored types.String) bool {
	if planned.IsUnknown() {
		return true
	}
	return !planned.Equal(stored)
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

	// The API can't hand back a token, but it does report whether the
	// one in use has been rotated out. That's enough to tell the
	// practitioner their stored secret is on a deadline, which is the
	// part that actually costs them if it goes unnoticed.
	if found.RotationPending() {
		resp.Diagnostics.AddWarning(
			"Gateway Token Rotation Pending",
			fmt.Sprintf("This Gateway's token was rotated out at %s and it has not yet connected "+
				"with the replacement. The token in Terraform state keeps working only until the "+
				"Gateway picks up the replacement or the API's grace period elapses, whichever "+
				"comes first - after which the Gateway is stranded.\n\n"+
				"If the rotation was performed outside Terraform, the replacement secret cannot be "+
				"recovered: the API returns it once. Rotate through token_rotation_trigger so the "+
				"new value lands in state.", found.RotatedAt.Format(time.RFC3339)),
		)
	}

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
	// site_id is RequiresReplace, so an update changes name, the
	// rotation trigger, or both. Carry the stored token through rather
	// than relying on the plan to have it (it won't - see
	// NewGatewayResource); the rotation below overwrites it when asked.
	plan.Token = state.Token

	updated, err := r.client.Sites.Gateways(plan.SiteID.ValueString()).Update(ctx, plan.ID.ValueString(), &firezone.UpdateGatewayRequest{
		Name: plan.Name.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Updating Gateway", err.Error())
		return
	}

	plan.Name = types.StringValue(updated.Name)

	if rotationTriggered(plan.TokenRotationTrigger, state.TokenRotationTrigger) {
		rotated, err := r.client.Sites.Gateways(plan.SiteID.ValueString()).
			RotateToken(ctx, plan.ID.ValueString())
		if err != nil {
			// The rename above may already have landed. Persist what we
			// know so the next plan sees the current name rather than
			// re-attempting it, and surface the rotation failure.
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			resp.Diagnostics.AddError("Error Rotating Gateway Token", err.Error())
			return
		}

		plan.Token = types.StringValue(rotated.Token)

		resp.Diagnostics.AddWarning(
			"Gateway Token Rotated",
			"A replacement token has been minted and is now in state. The Gateway keeps running "+
				"on its previous token until it connects with this one or the API's grace period "+
				"elapses, whichever comes first. Deliver the new token to the Gateway host and "+
				"restart it within that window, or the Gateway will be stranded.",
		)
	}

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
