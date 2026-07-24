package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-go"
)

var (
	_ resource.Resource                = &siteResource{}
	_ resource.ResourceWithImportState = &siteResource{}
	_ resource.ResourceWithConfigure   = &siteResource{}
)

// NewSiteResource returns a new firezone_site resource instance, for
// use with FirezoneProvider.Resources.
func NewSiteResource() resource.Resource {
	return &siteResource{}
}

type siteResource struct {
	client *firezone.Client
}

// siteResourceModel mirrors the firezone_site resource schema.
type siteResourceModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

func (r *siteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_site"
}

func (r *siteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Site - a logical grouping of Gateways and the Resources they expose.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Site ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Site name.",
			},
		},
	}
}

func (r *siteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	r.client = client
}

func (r *siteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan siteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	site, err := r.client.Sites.Create(ctx, &firezone.CreateSiteRequest{Name: plan.Name.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("Error Creating Site", err.Error())
		return
	}

	plan.ID = types.StringValue(site.ID)
	plan.Name = types.StringValue(site.Name)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *siteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state siteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	site, err := r.client.Sites.Get(ctx, state.ID.ValueString())
	if err != nil {
		if firezone.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error Reading Site", err.Error())
		return
	}

	state.Name = types.StringValue(site.Name)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *siteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan siteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	site, err := r.client.Sites.Update(ctx, plan.ID.ValueString(), &firezone.UpdateSiteRequest{Name: plan.Name.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("Error Updating Site", err.Error())
		return
	}

	plan.Name = types.StringValue(site.Name)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *siteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state siteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.Sites.Delete(ctx, state.ID.ValueString()); err != nil && !firezone.IsNotFound(err) {
		resp.Diagnostics.AddError("Error Deleting Site", err.Error())
	}
}

func (r *siteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, idPath, req, resp)
}
