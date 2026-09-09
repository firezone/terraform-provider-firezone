package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

var (
	_ resource.Resource                   = &actorResource{}
	_ resource.ResourceWithImportState    = &actorResource{}
	_ resource.ResourceWithConfigure      = &actorResource{}
	_ resource.ResourceWithValidateConfig = &actorResource{}
)

// Actor types, as accepted by the type attribute's OneOf validator.
const (
	actorTypeUser           = "account_user"
	actorTypeAdminUser      = "account_admin_user"
	actorTypeServiceAccount = "service_account"
)

// NewActorResource returns a new firezone_actor resource instance, for
// use with FirezoneProvider.Resources.
func NewActorResource() resource.Resource {
	return &actorResource{}
}

type actorResource struct {
	client *firezone.Client
}

// actorResourceModel mirrors the firezone_actor resource schema.
type actorResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Email               types.String `tfsdk:"email"`
	Type                types.String `tfsdk:"type"`
	AllowEmailOTPSignIn types.Bool   `tfsdk:"allow_email_otp_sign_in"`
	Enabled             types.Bool   `tfsdk:"enabled"`
}

func (r *actorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_actor"
}

func (r *actorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An Actor - a user, admin, or service account.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Actor ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Actor name.",
			},
			"email": schema.StringAttribute{
				Optional:    true,
				Description: "Email address. Required for account_user and account_admin_user; must be omitted for service_account.",
				Validators: []validator.String{
					// The API stores an empty string as null, so a config that
					// sets one can never match the value read back. Say so at
					// plan time rather than failing the apply with Terraform's
					// generic inconsistent-result error. Omit the argument to
					// leave the field unset.
					stringvalidator.LengthAtLeast(1),
				},
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "One of account_user, account_admin_user, service_account. api_client also exists but cannot be managed via this API.",
				Validators: []validator.String{
					stringvalidator.OneOf(actorTypeUser, actorTypeAdminUser, actorTypeServiceAccount),
				},
			},
			"allow_email_otp_sign_in": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Whether this Actor may sign in via a one-time passcode emailed to them.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the Actor is enabled. Disabling revokes all of its active Client tokens and portal sessions immediately.",
			},
		},
	}
}

// ValidateConfig enforces email's dependence on type: it is required
// for account_user and account_admin_user, and must be omitted for
// service_account. The schema can't express that - email is Optional
// either way, and neither attribute's validator can see the other.
//
// Without this the mistake surfaces as a 422 at apply time, after
// earlier resources in the same apply have already been created - and
// the API reports it against "type", which is the attribute the
// practitioner got right.
func (r *actorResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config actorResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Either can come from an expression unresolved until apply. Defer
	// rather than guess - the API still enforces the rule.
	if config.Type.IsUnknown() || config.Email.IsUnknown() {
		return
	}
	if config.Type.IsNull() {
		return
	}

	actorType := config.Type.ValueString()
	hasEmail := !config.Email.IsNull()

	switch actorType {
	case actorTypeServiceAccount:
		if hasEmail {
			resp.Diagnostics.AddAttributeError(
				emailPath,
				"Invalid Attribute Combination",
				"email must be omitted when type is \""+actorTypeServiceAccount+"\". "+
					"A service account has no mailbox, and the API rejects the request "+
					"outright - reporting the error against \"type\" rather than here.",
			)
		}
	case actorTypeUser, actorTypeAdminUser:
		if !hasEmail {
			resp.Diagnostics.AddAttributeError(
				emailPath,
				"Missing Required Attribute",
				"email is required when type is \""+actorType+"\". "+
					"Only \""+actorTypeServiceAccount+"\" Actors omit it. The API rejects the "+
					"request outright - reporting the error against \"type\" rather than here.",
			)
		}
	}
	// An unrecognised type is already reported by the attribute's own
	// OneOf validator; there is no rule to apply to it here.
}

func (r *actorResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	r.client = client
}

func (r *actorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan actorResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	allowOTP := plan.AllowEmailOTPSignIn.ValueBool()
	created, err := r.client.Actors.Create(ctx, &firezone.CreateActorRequest{
		Name:                plan.Name.ValueString(),
		Type:                firezone.ActorType(plan.Type.ValueString()),
		Email:               plan.Email.ValueString(),
		AllowEmailOTPSignIn: &allowOTP,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Creating Actor", err.Error())
		return
	}

	// A newly created Actor is always enabled; only call Disable if the
	// plan asked for it, to keep the happy path (enabled=true, the
	// default) to a single API call.
	if !plan.Enabled.ValueBool() {
		created, err = r.client.Actors.Disable(ctx, created.ID)
		if err != nil {
			resp.Diagnostics.AddError("Error Disabling Newly Created Actor", err.Error())
			return
		}
	}

	actorModelFromAPI(created, &plan)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *actorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state actorResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, err := r.client.Actors.Get(ctx, state.ID.ValueString())
	if err != nil {
		if firezone.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error Reading Actor", err.Error())
		return
	}

	actorModelFromAPI(found, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *actorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan actorResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state actorResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	allowOTP := plan.AllowEmailOTPSignIn.ValueBool()
	updated, err := r.client.Actors.Update(ctx, plan.ID.ValueString(), &firezone.UpdateActorRequest{
		Name:                plan.Name.ValueString(),
		Type:                firezone.ActorType(plan.Type.ValueString()),
		Email:               nullableString(plan.Email),
		AllowEmailOTPSignIn: &allowOTP,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Updating Actor", err.Error())
		return
	}

	if plan.Enabled.ValueBool() != state.Enabled.ValueBool() {
		if plan.Enabled.ValueBool() {
			updated, err = r.client.Actors.Enable(ctx, plan.ID.ValueString())
		} else {
			updated, err = r.client.Actors.Disable(ctx, plan.ID.ValueString())
		}
		if err != nil {
			resp.Diagnostics.AddError("Error Changing Actor Enabled State", err.Error())
			return
		}
	}

	actorModelFromAPI(updated, &plan)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *actorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state actorResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.Actors.Delete(ctx, state.ID.ValueString()); err != nil && !firezone.IsNotFound(err) {
		resp.Diagnostics.AddError("Error Deleting Actor", err.Error())
	}
}

func (r *actorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, idPath, req, resp)
}

func actorModelFromAPI(actor *firezone.Actor, model *actorResourceModel) {
	model.ID = types.StringValue(actor.ID)
	model.Name = types.StringValue(actor.Name)
	model.Type = types.StringValue(string(actor.Type))
	if actor.Email == "" {
		model.Email = types.StringNull()
	} else {
		model.Email = types.StringValue(actor.Email)
	}
	model.AllowEmailOTPSignIn = types.BoolValue(actor.AllowEmailOTPSignIn)
	model.Enabled = types.BoolValue(!actor.IsDisabled)
}
