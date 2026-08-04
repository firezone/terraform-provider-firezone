package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-go"
)

var (
	_ resource.Resource                   = &resourceResource{}
	_ resource.ResourceWithImportState    = &resourceResource{}
	_ resource.ResourceWithConfigure      = &resourceResource{}
	_ resource.ResourceWithValidateConfig = &resourceResource{}
)

// resourceTypeStaticDevicePool is the one Resource type that is not
// attached to a Site. The API nulls site_id for it server-side, so the
// provider has to treat site_id as forbidden rather than required here
// - see ValidateConfig.
const resourceTypeStaticDevicePool = "static_device_pool"

// NewResourceResource returns a new firezone_resource resource
// instance, for use with FirezoneProvider.Resources.
func NewResourceResource() resource.Resource {
	return &resourceResource{}
}

type resourceResource struct {
	client *firezone.Client
}

// resourceFilterModel mirrors a firezone_resource resource's nested
// filters block.
type resourceFilterModel struct {
	Protocol types.String `tfsdk:"protocol"`
	Ports    types.List   `tfsdk:"ports"`
}

// resourceResourceModel mirrors the firezone_resource resource schema.
type resourceResourceModel struct {
	ID                 types.String          `tfsdk:"id"`
	SiteID             types.String          `tfsdk:"site_id"`
	Name               types.String          `tfsdk:"name"`
	Type               types.String          `tfsdk:"type"`
	Address            types.String          `tfsdk:"address"`
	AddressDescription types.String          `tfsdk:"address_description"`
	IPStack            types.String          `tfsdk:"ip_stack"`
	Filters            []resourceFilterModel `tfsdk:"filters"`
}

func (r *resourceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource"
}

func (r *resourceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Resource - a network object (CIDR, IP, DNS name, or static device pool) that Policies grant access to.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resource ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"site_id": schema.StringAttribute{
				Optional: true,
				Description: "ID of the Site this Resource belongs to. Required for every type " +
					"except static_device_pool, which is not attached to a Site and must omit it.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Resource name.",
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "Resource type. One of cidr, ip, dns, static_device_pool. \"internet\" also exists but is API-read-only and cannot be set here.",
				Validators: []validator.String{
					stringvalidator.OneOf("cidr", "ip", "dns", "static_device_pool"),
				},
			},
			"address": schema.StringAttribute{
				Optional:    true,
				Description: "Resource address (CIDR, IP, or DNS name, depending on type).",
			},
			"address_description": schema.StringAttribute{
				Optional:    true,
				Description: "Human-readable description of the address.",
			},
			"ip_stack": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "IP family constraint. One of ipv4_only, ipv6_only, dual.",
				Validators: []validator.String{
					stringvalidator.OneOf("ipv4_only", "ipv6_only", "dual"),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"filters": schema.ListNestedBlock{
				Description: "Traffic filters restricting the protocols and ports this Resource exposes.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"protocol": schema.StringAttribute{
							Required:    true,
							Description: "Transport protocol. One of tcp, udp, icmp.",
							Validators: []validator.String{
								stringvalidator.OneOf("tcp", "udp", "icmp"),
							},
						},
						"ports": schema.ListAttribute{
							ElementType: types.StringType,
							Optional:    true,
							Description: "Port numbers or ranges (e.g. \"80\" or \"8000 - 9000\"). Not applicable to icmp.",
						},
					},
				},
			},
		},
	}
}

// ValidateConfig enforces the API's conditional requirement on site_id:
// required for every Resource type except static_device_pool, which is
// not attached to a Site at all.
//
// This can't be expressed with a plain Required/Optional flag, and it
// matters in both directions. Omitting site_id on a normal Resource is
// a 422 at apply time. Setting it on a static_device_pool is worse: the
// API silently discards it, so the apply succeeds while state records a
// Site the server never stored.
func (r *resourceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config resourceResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Either value can come from an expression that isn't resolved until
	// apply. Defer rather than guess - the API still enforces this.
	if config.Type.IsUnknown() || config.SiteID.IsUnknown() {
		return
	}

	isPool := config.Type.ValueString() == resourceTypeStaticDevicePool
	hasSiteID := !config.SiteID.IsNull()

	switch {
	case isPool && hasSiteID:
		resp.Diagnostics.AddAttributeError(
			siteIDPath,
			"Invalid Attribute Combination",
			"site_id must be omitted when type is \""+resourceTypeStaticDevicePool+"\". "+
				"A static device pool is not attached to a Site, and the API discards any "+
				"site_id sent with one - leaving Terraform state holding a Site the server "+
				"does not have.",
		)
	case !isPool && !hasSiteID:
		resp.Diagnostics.AddAttributeError(
			siteIDPath,
			"Missing Required Attribute",
			"site_id is required when type is \""+config.Type.ValueString()+"\". "+
				"Only \""+resourceTypeStaticDevicePool+"\" Resources omit it.",
		)
	}
}

func (r *resourceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	r.client = client
}

func (r *resourceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan resourceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	filters, diags := filtersFromModel(ctx, plan.Filters)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.Resources.Create(ctx, &firezone.CreateResourceRequest{
		Name:               plan.Name.ValueString(),
		Type:               firezone.ResourceType(plan.Type.ValueString()),
		Address:            plan.Address.ValueString(),
		AddressDescription: plan.AddressDescription.ValueString(),
		IPStack:            firezone.IPStack(plan.IPStack.ValueString()),
		SiteID:             plan.SiteID.ValueString(),
		Filters:            filters,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Creating Resource", err.Error())
		return
	}

	resp.Diagnostics.Append(resourceModelFromAPI(ctx, created, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *resourceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resourceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, err := r.client.Resources.Get(ctx, state.ID.ValueString())
	if err != nil {
		if firezone.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error Reading Resource", err.Error())
		return
	}

	resp.Diagnostics.Append(resourceModelFromAPI(ctx, found, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *resourceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan resourceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	filters, diags := filtersFromModel(ctx, plan.Filters)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	updated, err := r.client.Resources.Update(ctx, plan.ID.ValueString(), &firezone.UpdateResourceRequest{
		Name:               plan.Name.ValueString(),
		Type:               firezone.ResourceType(plan.Type.ValueString()),
		Address:            plan.Address.ValueString(),
		AddressDescription: plan.AddressDescription.ValueString(),
		IPStack:            firezone.IPStack(plan.IPStack.ValueString()),
		SiteID:             plan.SiteID.ValueString(),
		Filters:            filters,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Updating Resource", err.Error())
		return
	}

	resp.Diagnostics.Append(resourceModelFromAPI(ctx, updated, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *resourceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resourceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.Resources.Delete(ctx, state.ID.ValueString()); err != nil && !firezone.IsNotFound(err) {
		resp.Diagnostics.AddError("Error Deleting Resource", err.Error())
	}
}

func (r *resourceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, idPath, req, resp)
}

// resourceModelFromAPI populates model's read-back fields from an API
// *firezone.Resource.
func resourceModelFromAPI(ctx context.Context, res *firezone.Resource, model *resourceResourceModel) (diags fwDiagnostics) {
	model.ID = types.StringValue(res.ID)
	model.Name = types.StringValue(res.Name)
	model.Type = types.StringValue(string(res.Type))
	model.Address = types.StringValue(res.Address)
	if res.AddressDescription == "" {
		model.AddressDescription = types.StringNull()
	} else {
		model.AddressDescription = types.StringValue(res.AddressDescription)
	}
	// The API omits ip_stack entirely for Resource types it doesn't
	// apply to (e.g. cidr, ip) - map that to an explicit null rather
	// than leaving the Computed attribute Unknown, which Terraform
	// rejects as an inconsistent post-apply result.
	if res.IPStack == "" {
		model.IPStack = types.StringNull()
	} else {
		model.IPStack = types.StringValue(string(res.IPStack))
	}
	// The API omits site_id for Resources that have none - i.e.
	// static_device_pool, which it detaches from any Site server-side.
	// Map that to an explicit null instead of keeping whatever the
	// caller planned, so state reflects the server rather than the
	// config. ValidateConfig already rejects the combination that would
	// make this a surprise.
	if res.SiteID == "" {
		model.SiteID = types.StringNull()
	} else {
		model.SiteID = types.StringValue(res.SiteID)
	}

	filters, filterDiags := filtersToModel(ctx, res.Filters)
	diags.Append(filterDiags...)
	model.Filters = filters

	return diags
}

func filtersFromModel(ctx context.Context, filters []resourceFilterModel) ([]firezone.Filter, fwDiagnostics) {
	var diags fwDiagnostics
	result := make([]firezone.Filter, 0, len(filters))
	for _, f := range filters {
		var ports []string
		diags.Append(f.Ports.ElementsAs(ctx, &ports, false)...)
		result = append(result, firezone.Filter{
			Protocol: firezone.FilterProtocol(f.Protocol.ValueString()),
			Ports:    ports,
		})
	}
	return result, diags
}

func filtersToModel(ctx context.Context, filters []firezone.Filter) ([]resourceFilterModel, fwDiagnostics) {
	var diags fwDiagnostics
	result := make([]resourceFilterModel, 0, len(filters))
	for _, f := range filters {
		ports, portDiags := types.ListValueFrom(ctx, types.StringType, f.Ports)
		diags.Append(portDiags...)
		result = append(result, resourceFilterModel{
			Protocol: types.StringValue(string(f.Protocol)),
			Ports:    ports,
		})
	}
	if len(result) == 0 {
		return nil, diags
	}
	return result, diags
}
