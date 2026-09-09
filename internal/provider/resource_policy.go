package provider

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	// Embeds the IANA timezone database so current_utc_datetime values
	// validate identically on hosts without a system zoneinfo - notably
	// Windows, where time.LoadLocation would otherwise fail for every
	// zone.
	_ "time/tzdata"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
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
	_ resource.Resource                   = &policyResource{}
	_ resource.ResourceWithImportState    = &policyResource{}
	_ resource.ResourceWithConfigure      = &policyResource{}
	_ resource.ResourceWithValidateConfig = &policyResource{}
)

// validOperatorsForProperty mirrors
// Portal.Policies.Condition.valid_operators_for_property/1. Property and
// operator are each valid on their own but only meaningful in specific
// pairs, so attribute-level OneOf validators can't express this - see
// ValidateConfig.
//
// Keep in sync with the API: a pair missing here is rejected at plan
// time even though the server would accept it.
var validOperatorsForProperty = map[string][]string{
	"remote_ip_location_region": {"is_in", "is_not_in"},
	"remote_ip":                 {"is_in_cidr", "is_not_in_cidr"},
	"auth_provider_id":          {"is_in", "is_not_in"},
	propertyCurrentUTCDatetime:  {"is_in_day_of_week_time_ranges"},
	"client_verified":           {"is"},
}

// NewPolicyResource returns a new firezone_policy resource instance,
// for use with FirezoneProvider.Resources.
//
// This resource does not yet expose an "enabled" attribute. It couldn't
// before: Policy responses carried no enabled/disabled state, so the
// value would have been settable but never verifiable. That's no longer
// true - PortalAPI.PolicyJSON now returns is_disabled, and the API takes
// it on update - so the attribute is now implementable and simply hasn't
// been added.
//
// Do not reintroduce calls to POST /policies/{id}/enable or /disable:
// those endpoints were removed in favor of the generic update.
func NewPolicyResource() resource.Resource {
	return &policyResource{}
}

type policyResource struct {
	client *firezone.Client
}

// policyConditionModel mirrors a firezone_policy resource's nested
// condition block.
type policyConditionModel struct {
	Property types.String `tfsdk:"property"`
	Operator types.String `tfsdk:"operator"`
	Values   types.List   `tfsdk:"values"`
}

// policyResourceModel mirrors the firezone_policy resource schema.
type policyResourceModel struct {
	ID                    types.String           `tfsdk:"id"`
	GroupID               types.String           `tfsdk:"group_id"`
	ResourceID            types.String           `tfsdk:"resource_id"`
	Description           types.String           `tfsdk:"description"`
	FlowLogUploadsEnabled types.Bool             `tfsdk:"flow_log_uploads_enabled"`
	Condition             []policyConditionModel `tfsdk:"condition"`
}

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy"
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Policy - grants a Group access to a Resource, optionally restricted by conditions.\n\n" +
			"At most one Policy may exist per (group_id, resource_id) pair. To grant a Group access " +
			"to several Resources, write one Policy per Resource; to grant several Groups access to " +
			"one Resource, write one Policy per Group. Two Policies on the same pair do not combine: " +
			"the API rejects the second one. Conditions restricting the same grant belong on a single " +
			"Policy, where they apply together.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Policy ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"group_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the Group being granted access.",
			},
			"resource_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the Resource being granted.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Human-readable description of why this access is granted.",
				Validators: []validator.String{
					// The API stores an empty string as null, so a config that
					// sets one can never match the value read back. Say so at
					// plan time rather than failing the apply with Terraform's
					// generic inconsistent-result error. Omit the argument to
					// leave the field unset.
					stringvalidator.LengthAtLeast(1),
				},
			},
			"flow_log_uploads_enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether flow logs are uploaded for connections authorized by this Policy.",
			},
		},
		Blocks: map[string]schema.Block{
			"condition": schema.ListNestedBlock{
				Description: "Restricts when this Policy grants access. See the Firezone API's policy documentation for which operators are valid for each property and how values is interpreted.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"property": schema.StringAttribute{
							Required:    true,
							Description: "One of remote_ip_location_region, remote_ip, auth_provider_id, current_utc_datetime, client_verified.",
							Validators: []validator.String{
								stringvalidator.OneOf(
									"remote_ip_location_region",
									"remote_ip",
									"auth_provider_id",
									"current_utc_datetime",
									"client_verified",
								),
							},
						},
						"operator": schema.StringAttribute{
							Required: true,
							Description: "One of is_in, is_not_in, is_in_cidr, is_not_in_cidr, " +
								"is_in_day_of_week_time_ranges, is. Which are valid depends on property.",
							Validators: []validator.String{
								stringvalidator.OneOf(
									"is_in",
									"is_not_in",
									"is_in_cidr",
									"is_not_in_cidr",
									"is_in_day_of_week_time_ranges",
									"is",
								),
							},
						},
						"values": schema.ListAttribute{
							ElementType: types.StringType,
							Required:    true,
							Description: "Values to compare against, interpreted per property. For " +
								"current_utc_datetime, each value is a \"DAY/TIME_RANGES/TIMEZONE\" string " +
								"where DAY is one of M T W R F S U, TIME_RANGES is a comma-separated list " +
								"of HH:MM-HH:MM ranges, and TIMEZONE is an IANA timezone name - e.g. " +
								"\"M/09:00-17:00/America/New_York\". One value per day.",
						},
					},
				},
			},
		},
	}
}

// ValidateConfig rejects condition blocks whose operator doesn't apply
// to their property. Both attributes carry OneOf validators already, but
// those check each value in isolation - "current_utc_datetime" with
// "is_in" passes both and is still nonsense.
//
// Without this the mistake surfaces as a 422 at apply time, after
// earlier resources in the same apply have already been created.
func (r *policyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config policyResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Records the index of the first condition seen for each property,
	// so a later duplicate can point back at it.
	seen := make(map[string]int, len(config.Condition))

	for i, condition := range config.Condition {
		// Either value can come from an expression unresolved until
		// apply. Defer rather than guess - the API still enforces this.
		if condition.Property.IsUnknown() || condition.Operator.IsUnknown() {
			continue
		}
		if condition.Property.IsNull() || condition.Operator.IsNull() {
			continue
		}

		property := condition.Property.ValueString()
		operator := condition.Operator.ValueString()

		valid, known := validOperatorsForProperty[property]
		if !known {
			// The property's own OneOf validator already reported this.
			continue
		}

		if !slices.Contains(valid, operator) {
			resp.Diagnostics.AddAttributeError(
				path.Root("condition").AtListIndex(i).AtName("operator"),
				"Invalid Attribute Combination",
				fmt.Sprintf("operator %q does not apply to property %q. Valid operators for %q: %s.",
					operator, property, property, strings.Join(valid, ", ")),
			)
		}

		if first, dup := seen[property]; dup {
			resp.Diagnostics.AddAttributeError(
				path.Root("condition").AtListIndex(i).AtName("property"),
				"Duplicate Condition Property",
				fmt.Sprintf("property %q is already constrained by the condition at index %d. "+
					"The API allows at most one condition per property - combine the values "+
					"into a single block.", property, first),
			)
		} else {
			seen[property] = i
		}

		if property == propertyCurrentUTCDatetime {
			validateDayTimeRangeValues(ctx, condition.Values, i, &resp.Diagnostics)
		}
	}
}

// dayLetters are the day-of-week codes accepted in the leading segment
// of a current_utc_datetime value, mirroring the API's own set.
const dayLetters = "MTWRFSU"

// propertyCurrentUTCDatetime is the one condition property whose values
// carry structure the provider can check.
const propertyCurrentUTCDatetime = "current_utc_datetime"

// validateDayTimeRangeValues checks each element of a
// current_utc_datetime condition's values against the
// "DAY/TIME_RANGES/TIMEZONE" format.
//
// The API rejects a malformed value with a bare 422 naming no field, so
// a Policy carrying a value per weekday says nothing about which one is
// wrong. Report the offending element and its index instead.
func validateDayTimeRangeValues(ctx context.Context, values types.List, conditionIndex int, diags *fwDiagnostics) {
	if values.IsUnknown() || values.IsNull() {
		return
	}

	var elements []types.String
	// Deliberately dropped: a conversion failure here means the list
	// isn't a list of strings, which the schema already rejects.
	if d := values.ElementsAs(ctx, &elements, false); d.HasError() {
		return
	}

	for j, element := range elements {
		if element.IsUnknown() || element.IsNull() {
			continue
		}

		err := parseDayTimeRange(element.ValueString())
		if err == nil {
			continue
		}

		diags.AddAttributeError(
			path.Root("condition").AtListIndex(conditionIndex).AtName("values").AtListIndex(j),
			"Invalid Condition Value",
			fmt.Sprintf("%q is not a valid %s value: %s. Expected "+
				"\"DAY/TIME_RANGES/TIMEZONE\" - DAY one of %s, TIME_RANGES a comma-separated "+
				"list of HH:MM-HH:MM ranges, TIMEZONE an IANA name, e.g. "+
				"\"M/09:00-17:00/America/New_York\".",
				element.ValueString(), propertyCurrentUTCDatetime, err, strings.Join(strings.Split(dayLetters, ""), " ")),
		)
	}
}

// parseDayTimeRange reports why value is not a well-formed
// "DAY/TIME_RANGES/TIMEZONE" string, or nil if it is.
func parseDayTimeRange(value string) error {
	// The timezone name contains slashes of its own, so everything past
	// the second separator belongs to it.
	parts := strings.SplitN(value, "/", 3)
	if len(parts) != 3 {
		return fmt.Errorf("expected 3 \"/\"-separated segments, got %d", len(parts))
	}
	day, ranges, timezone := parts[0], parts[1], parts[2]

	if len(day) != 1 || !strings.Contains(dayLetters, day) {
		return fmt.Errorf("day %q is not one of %s", day, strings.Join(strings.Split(dayLetters, ""), " "))
	}

	if ranges == "" {
		return fmt.Errorf("time ranges are empty")
	}
	for _, r := range strings.Split(ranges, ",") {
		if err := parseTimeRange(r); err != nil {
			return err
		}
	}

	if timezone == "" {
		return fmt.Errorf("timezone is empty")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("timezone %q is not an IANA timezone name", timezone)
	}

	return nil
}

// parseTimeRange checks a single "HH:MM-HH:MM" range.
func parseTimeRange(value string) error {
	start, end, ok := strings.Cut(value, "-")
	if !ok {
		return fmt.Errorf("time range %q is not \"HH:MM-HH:MM\"", value)
	}

	startMinutes, err := parseClockTime(start)
	if err != nil {
		return fmt.Errorf("time range %q: %w", value, err)
	}
	endMinutes, err := parseClockTime(end)
	if err != nil {
		return fmt.Errorf("time range %q: %w", value, err)
	}

	// An overnight window is the reversed range a practitioner is most
	// likely to write on purpose. The API does not wrap it - it has to
	// be written as two ranges, one per day.
	if startMinutes >= endMinutes {
		return fmt.Errorf("time range %q ends at or before it starts; a window crossing "+
			"midnight must be split across two day values", value)
	}

	return nil
}

// parseClockTime converts "HH:MM" to minutes past midnight.
func parseClockTime(value string) (int, error) {
	hh, mm, ok := strings.Cut(value, ":")
	if !ok || len(hh) != 2 || len(mm) != 2 {
		return 0, fmt.Errorf("time %q is not \"HH:MM\"", value)
	}

	hours, err := strconv.Atoi(hh)
	if err != nil || hours < 0 || hours > 23 {
		return 0, fmt.Errorf("hour %q is not in 00-23", hh)
	}
	minutes, err := strconv.Atoi(mm)
	if err != nil || minutes < 0 || minutes > 59 {
		return 0, fmt.Errorf("minute %q is not in 00-59", mm)
	}

	return hours*60 + minutes, nil
}

func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	r.client = client
}

func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	conditions, diags := conditionsFromModel(ctx, plan.Condition)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	flowLogUploadsEnabled := plan.FlowLogUploadsEnabled.ValueBool()
	created, err := r.client.Policies.Create(ctx, &firezone.CreatePolicyRequest{
		GroupID:               plan.GroupID.ValueString(),
		ResourceID:            plan.ResourceID.ValueString(),
		Description:           plan.Description.ValueString(),
		FlowLogUploadsEnabled: &flowLogUploadsEnabled,
		Conditions:            conditions,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Creating Policy", err.Error())
		return
	}

	resp.Diagnostics.Append(policyModelFromAPI(ctx, created, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, err := r.client.Policies.Get(ctx, state.ID.ValueString())
	if err != nil {
		if firezone.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error Reading Policy", err.Error())
		return
	}

	resp.Diagnostics.Append(policyModelFromAPI(ctx, found, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	conditions, diags := conditionsFromModel(ctx, plan.Condition)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	flowLogUploadsEnabled := plan.FlowLogUploadsEnabled.ValueBool()
	updated, err := r.client.Policies.Update(ctx, plan.ID.ValueString(), &firezone.UpdatePolicyRequest{
		GroupID:               plan.GroupID.ValueString(),
		ResourceID:            plan.ResourceID.ValueString(),
		Description:           nullableString(plan.Description),
		FlowLogUploadsEnabled: &flowLogUploadsEnabled,
		Conditions:            &conditions,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Updating Policy", err.Error())
		return
	}

	resp.Diagnostics.Append(policyModelFromAPI(ctx, updated, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.Policies.Delete(ctx, state.ID.ValueString()); err != nil && !firezone.IsNotFound(err) {
		resp.Diagnostics.AddError("Error Deleting Policy", err.Error())
	}
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, idPath, req, resp)
}

func policyModelFromAPI(ctx context.Context, pol *firezone.Policy, model *policyResourceModel) fwDiagnostics {
	model.ID = types.StringValue(pol.ID)
	model.GroupID = types.StringValue(pol.GroupID)
	model.ResourceID = types.StringValue(pol.ResourceID)
	// A Policy with no description reads back as "" from the API, but
	// the config that produced it said nothing at all. Mapping it to null
	// is what keeps the two consistent - Terraform rejects an apply whose
	// result turns a null Optional attribute into an empty string.
	if pol.Description == "" {
		model.Description = types.StringNull()
	} else {
		model.Description = types.StringValue(pol.Description)
	}
	model.FlowLogUploadsEnabled = types.BoolValue(pol.FlowLogUploadsEnabled)

	conditions, diags := conditionsToModel(ctx, pol.Conditions)
	model.Condition = conditions
	return diags
}

func conditionsFromModel(ctx context.Context, conditions []policyConditionModel) ([]firezone.Condition, fwDiagnostics) {
	var diags fwDiagnostics
	result := make([]firezone.Condition, 0, len(conditions))
	for _, c := range conditions {
		var values []string
		diags.Append(c.Values.ElementsAs(ctx, &values, false)...)
		result = append(result, firezone.Condition{
			Property: firezone.ConditionProperty(c.Property.ValueString()),
			Operator: firezone.ConditionOperator(c.Operator.ValueString()),
			Values:   values,
		})
	}
	return result, diags
}

func conditionsToModel(ctx context.Context, conditions []firezone.Condition) ([]policyConditionModel, fwDiagnostics) {
	var diags fwDiagnostics
	result := make([]policyConditionModel, 0, len(conditions))
	for _, c := range conditions {
		values, valueDiags := types.ListValueFrom(ctx, types.StringType, c.Values)
		diags.Append(valueDiags...)
		result = append(result, policyConditionModel{
			Property: types.StringValue(string(c.Property)),
			Operator: types.StringValue(string(c.Operator)),
			Values:   values,
		})
	}
	if len(result) == 0 {
		return nil, diags
	}
	return result, diags
}
