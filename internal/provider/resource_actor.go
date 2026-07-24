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

	firezone "github.com/firezone/firezone-go"
)

var (
	_ resource.Resource                = &actorResource{}
	_ resource.ResourceWithImportState = &actorResource{}
	_ resource.ResourceWithConfigure   = &actorResource{}
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
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "One of account_user, account_admin_user, service_account. api_client also exists but cannot be managed via this API.",
				Validators: []validator.String{
					stringvalidator.OneOf("account_user", "account_admin_user", "service_account"),
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
		Email:               plan.Email.ValueString(),
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
	model.Enabled = types.BoolValue(!actor.IsDisabled())
}
