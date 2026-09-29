package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

var (
	_ resource.Resource                   = &resourceResource{}
	_ resource.ResourceWithImportState    = &resourceResource{}
	_ resource.ResourceWithConfigure      = &resourceResource{}
	_ resource.ResourceWithValidateConfig = &resourceResource{}
	_ resource.ResourceWithModifyPlan     = &resourceResource{}
)

// Device pools have membership criteria instead of an address or Site.
const resourceTypeDevicePool = "device_pool"

// resourceTypeDNS is the only Resource type ip_stack applies to. The
// API defaults it to "dual" for dns Resources and enforces NULL for
// every other type via a check constraint - see ValidateConfig.
const resourceTypeDNS = "dns"

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
	DeviceMembershipCriteria types.Object          `tfsdk:"device_membership_criteria"`
	ID                       types.String          `tfsdk:"id"`
	SiteID                   types.String          `tfsdk:"site_id"`
	Name                     types.String          `tfsdk:"name"`
	Type                     types.String          `tfsdk:"type"`
	Address                  types.String          `tfsdk:"address"`
	AddressDescription       types.String          `tfsdk:"address_description"`
	IPStack                  types.String          `tfsdk:"ip_stack"`
	Filters                  []resourceFilterModel `tfsdk:"filters"`
}

func (r *resourceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource"
}

func (r *resourceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Resource - a network object (CIDR, IP, DNS name, or device pool) that Policies grant access to.",
		Attributes: map[string]schema.Attribute{
			"device_membership_criteria": deviceMembershipCriteriaSchema(),
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
					"except device_pool, which is not attached to a Site and must omit it.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Resource name.",
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "Resource type. One of cidr, ip, dns, device_pool. Internet Resources are API-read-only.",
				Validators: []validator.String{
					stringvalidator.OneOf("cidr", "ip", "dns", "device_pool"),
				},
			},
			"address": schema.StringAttribute{
				Optional:    true,
				Description: "Resource address (CIDR, IP, or DNS name, depending on type).",
				Validators: []validator.String{
					// The API stores an empty string as null, so a config that
					// sets one can never match the value read back. Say so at
					// plan time rather than failing the apply with Terraform's
					// generic inconsistent-result error. Omit the argument to
					// leave the field unset.
					stringvalidator.LengthAtLeast(1),
				},
			},
			"address_description": schema.StringAttribute{
				Optional:    true,
				Description: "Human-readable description of the address.",
				Validators: []validator.String{
					// The API stores an empty string as null, so a config that
					// sets one can never match the value read back. Say so at
					// plan time rather than failing the apply with Terraform's
					// generic inconsistent-result error. Omit the argument to
					// leave the field unset.
					stringvalidator.LengthAtLeast(1),
				},
			},
			"ip_stack": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "IP family constraint. One of ipv4_only, ipv6_only, dual. Applies " +
					"only to dns Resources, where it defaults to dual; must be omitted for " +
					"every other type.",
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

// ValidateConfig enforces the two attribute rules the API applies
// conditionally on type, neither of which a Required/Optional flag can
// express:
//
//   - site_id is required for every type except device_pool,
//     which is not attached to a Site at all.
//   - ip_stack applies only to dns Resources, and must be omitted for
//     every other type.
//
// Both matter because the failure modes differ. Omitting site_id on a
// normal Resource, or setting ip_stack on a non-dns one, is a 422 at
// apply time - after other resources in the same apply have already
// been created. Setting site_id on a device_pool is worse still:
// the API silently discards it, so the apply succeeds while state
// records a Site the server never stored.
func (r *resourceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config resourceResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// type drives both rules, so nothing can be checked without it. It
	// can come from an expression that isn't resolved until apply -
	// defer rather than guess, since the API still enforces both.
	if config.Type.IsUnknown() {
		return
	}
	resourceType := config.Type.ValueString()

	if !config.SiteID.IsUnknown() {
		isPool := resourceType == resourceTypeDevicePool
		hasSiteID := !config.SiteID.IsNull()

		switch {
		case isPool && hasSiteID:
			resp.Diagnostics.AddAttributeError(
				siteIDPath,
				"Invalid Attribute Combination",
				"site_id must be omitted when type is \""+resourceTypeDevicePool+"\". "+
					"A device pool is not attached to a Site, and the API discards any "+
					"site_id sent with one - leaving Terraform state holding a Site the server "+
					"does not have.",
			)
		case !isPool && !hasSiteID:
			resp.Diagnostics.AddAttributeError(
				siteIDPath,
				"Missing Required Attribute",
				"site_id is required when type is \""+resourceType+"\". "+
					"Only \""+resourceTypeDevicePool+"\" Resources omit it.",
			)
		}
	}

	if resourceType == resourceTypeDevicePool && !config.Address.IsNull() && !config.Address.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("address"), "Invalid Attribute Combination", "address must be omitted for a device_pool; the API discards it.")
	}
	validateDeviceMembershipCriteria(ctx, config, &resp.Diagnostics)

	// ip_stack has no "required" direction: the API defaults it to
	// "dual" for dns Resources, so omitting it is always valid.
	if !config.IPStack.IsUnknown() && !config.IPStack.IsNull() && resourceType != resourceTypeDNS {
		resp.Diagnostics.AddAttributeError(
			ipStackPath,
			"Invalid Attribute Combination",
			"ip_stack applies only to \""+resourceTypeDNS+"\" Resources and must be omitted "+
				"when type is \""+resourceType+"\". The API enforces this with a check "+
				"constraint and rejects the request outright.",
		)
	}
}

// ModifyPlan clears criteria when converting away from a pool. For a conversion
// into a pool with unmanaged membership, Create/Update initializes an empty list.
func (r *resourceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan, config resourceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || plan.Type.IsUnknown() {
		return
	}
	if plan.Type.ValueString() != resourceTypeDNS && config.IPStack.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, ipStackPath, types.StringNull())...)
	}
	if !config.DeviceMembershipCriteria.IsNull() {
		return
	}
	p := path.Root("device_membership_criteria")
	if plan.Type.ValueString() != resourceTypeDevicePool {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, p, types.ObjectNull(criteriaAttributeTypes))...)
	} else if !req.State.Raw.IsNull() {
		var state resourceResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if state.Type.ValueString() != resourceTypeDevicePool {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, p, types.ObjectUnknown(criteriaAttributeTypes))...)
		}
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

	criteria, criteriaDiags := criteriaForCreate(ctx, plan)
	resp.Diagnostics.Append(criteriaDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.Resources.Create(ctx, &firezone.CreateResourceRequest{
		DeviceMembershipCriteria: criteria,
		Name:                     plan.Name.ValueString(),
		Type:                     firezone.ResourceType(plan.Type.ValueString()),
		Address:                  plan.Address.ValueString(),
		AddressDescription:       plan.AddressDescription.ValueString(),
		IPStack:                  firezone.IPStack(plan.IPStack.ValueString()),
		SiteID:                   plan.SiteID.ValueString(),
		Filters:                  filters,
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

	// Unconfigured criteria are owned by pool-member resources or the portal.
	// Never echo their last read value back during a rename or filter update.
	var config resourceResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	var criteria []byte
	if !config.DeviceMembershipCriteria.IsNull() {
		var criteriaDiags fwDiagnostics
		criteria, criteriaDiags = criteriaToAPI(ctx, plan.DeviceMembershipCriteria)
		resp.Diagnostics.Append(criteriaDiags...)
	} else if plan.Type.ValueString() == resourceTypeDevicePool {
		var state resourceResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if state.Type.ValueString() != resourceTypeDevicePool {
			criteria = emptyListedCriteria
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	updated, err := r.client.Resources.Update(ctx, plan.ID.ValueString(), &firezone.UpdateResourceRequest{
		DeviceMembershipCriteria: criteria,
		Name:                     plan.Name.ValueString(),
		Type:                     firezone.ResourceType(plan.Type.ValueString()),
		Address:                  nullableString(plan.Address),
		AddressDescription:       nullableString(plan.AddressDescription),
		IPStack:                  ipStackForUpdate(plan.IPStack),
		SiteID:                   nullableString(plan.SiteID),
		Filters:                  &filters,
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
	criteria, criteriaDiags := criteriaFromAPI(ctx, res.DeviceMembershipCriteria, model.DeviceMembershipCriteria)
	diags.Append(criteriaDiags...)
	model.DeviceMembershipCriteria = criteria

	model.ID = types.StringValue(res.ID)
	model.Name = types.StringValue(res.Name)
	model.Type = types.StringValue(string(res.Type))
	// The API nulls address for types that don't have one -
	// device_pool and internet. An Optional attribute the config
	// left unset must read back as null, not "", or Terraform rejects
	// the apply as an inconsistent result.
	if res.Address == "" {
		model.Address = types.StringNull()
	} else {
		model.Address = types.StringValue(res.Address)
	}
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
	// device_pool, which it detaches from any Site server-side.
	// Map that to an explicit null instead of keeping whatever the
	// caller planned, so state reflects the server rather than the
	// config. ValidateConfig already rejects the combination that would
	// make this a surprise.
	if res.SiteID == "" {
		model.SiteID = types.StringNull()
	} else {
		model.SiteID = types.StringValue(res.SiteID)
	}

	// Pass the planned/prior filters in so ports can tell an omitted
	// argument from an explicit empty list - see portsToModel.
	filters, filterDiags := filtersToModel(ctx, res.Filters, model.Filters)
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

// ipStackForUpdate maps the planned ip_stack onto the SDK's tri-state
// update field.
//
// Unlike the other nullable attributes, ip_stack is never cleared. It
// applies only to dns Resources, where the API always has a value for
// it, and the API rejects the field outright on every other type - so
// a JSON null would turn "this Resource has no IP stack" into a 422.
// Null or unknown therefore means omit, not clear.
func ipStackForUpdate(v types.String) *firezone.Null[firezone.IPStack] {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	return firezone.Set(firezone.IPStack(v.ValueString()))
}

// filtersToModel maps the API's filters back into the model. prior is
// the planned (on create/update) or stored (on read) filter list, used
// only to disambiguate empty ports - see portsToModel.
func filtersToModel(ctx context.Context, filters []firezone.Filter, prior []resourceFilterModel) ([]resourceFilterModel, fwDiagnostics) {
	var diags fwDiagnostics
	result := make([]resourceFilterModel, 0, len(filters))
	for i, f := range filters {
		ports, portDiags := portsToModel(ctx, f.Ports, priorPorts(prior, i))
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

// portsToModel maps one filter's ports back into the model.
//
// The API always returns a ports array, sending [] for a filter that
// has none - an icmp filter, most commonly. Terraform, though,
// distinguishes an omitted ports argument (null) from an explicit empty
// list, so echoing [] where the config said nothing at all is an
// inconsistent-result error.
//
// Resolve it by keeping whatever the plan or prior state held whenever
// the API reports no ports, since null and [] mean the same thing to
// the API. With nothing to fall back on - during import - default to
// null, matching how a config that simply omits ports round-trips.
func portsToModel(ctx context.Context, ports []string, prior *types.List) (types.List, fwDiagnostics) {
	if len(ports) == 0 {
		if prior != nil {
			return *prior, nil
		}
		return types.ListNull(types.StringType), nil
	}
	return types.ListValueFrom(ctx, types.StringType, ports)
}

// priorPorts returns the ports value the caller planned or stored for
// the filter at index i, or nil when there isn't one - which happens on
// import, and whenever the API returns more filters than were sent.
//
// Matching by index is sound because the API preserves filter order:
// they're an Ecto embeds_many, stored and returned in the order given.
func priorPorts(prior []resourceFilterModel, i int) *types.List {
	if i >= len(prior) {
		return nil
	}
	return &prior[i].Ports
}
