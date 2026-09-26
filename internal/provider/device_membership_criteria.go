package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

var criteriaAttributeTypes = map[string]attr.Type{
	"mode":       types.StringType,
	"device_ids": types.SetType{ElemType: types.StringType},
	"group_id":   types.StringType,
}

var emptyListedCriteria = json.RawMessage(`{"device":{"field":"id","op":"in","value":[]}}`)

type deviceMembershipCriteriaModel struct {
	Mode      types.String `tfsdk:"mode"`
	DeviceIDs types.Set    `tfsdk:"device_ids"`
	GroupID   types.String `tfsdk:"group_id"`
}

func deviceMembershipCriteriaSchema() schema.SingleNestedAttribute {
	uuidValidator := stringvalidator.RegexMatches(regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`), "must be a lowercase UUID")
	return schema.SingleNestedAttribute{
		Optional: true, Computed: true,
		PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
		Description:   "Membership criteria for a device_pool. Omit to create an empty listed pool whose members are managed by firezone_pool_member; later updates leave criteria unchanged. When configured, this attribute owns the entire membership rule: do not also use firezone_pool_member for the same pool. Changing a dynamic rule drops active connections through the pool.",
		Attributes: map[string]schema.Attribute{
			"mode":       schema.StringAttribute{Required: true, Description: "One of listed, own_devices, all_devices, actor_group. own_devices selects the requesting Actor's Clients; all_devices selects every Client in the Account.", Validators: []validator.String{stringvalidator.OneOf("listed", "own_devices", "all_devices", "actor_group")}},
			"device_ids": schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Client UUIDs for listed mode. Omit or use an empty set for no members. Must be omitted for other modes.", Validators: []validator.Set{setvalidator.ValueStringsAre(uuidValidator)}},
			"group_id":   schema.StringAttribute{Optional: true, Description: "Group UUID, required only for actor_group mode.", Validators: []validator.String{uuidValidator}},
		},
	}
}

func validateDeviceMembershipCriteria(ctx context.Context, config resourceResourceModel, diags *fwDiagnostics) {
	value := config.DeviceMembershipCriteria
	if value.IsNull() || value.IsUnknown() {
		return
	}
	p := path.Root("device_membership_criteria")
	if config.Type.ValueString() != resourceTypeDevicePool {
		diags.AddAttributeError(p, "Invalid Attribute Combination", "device_membership_criteria applies only to device_pool Resources.")
		return
	}
	var criteria deviceMembershipCriteriaModel
	diags.Append(value.As(ctx, &criteria, basetypes.ObjectAsOptions{})...)
	if diags.HasError() || criteria.Mode.IsUnknown() {
		return
	}
	mode := criteria.Mode.ValueString()
	if mode != "listed" && !criteria.DeviceIDs.IsNull() && !criteria.DeviceIDs.IsUnknown() {
		diags.AddAttributeError(p.AtName("device_ids"), "Invalid Attribute Combination", "device_ids must be omitted unless mode is listed.")
	}
	if !criteria.GroupID.IsUnknown() {
		if mode == "actor_group" && criteria.GroupID.IsNull() {
			diags.AddAttributeError(p.AtName("group_id"), "Missing Required Attribute", "group_id is required for actor_group mode.")
		} else if mode != "actor_group" && !criteria.GroupID.IsNull() {
			diags.AddAttributeError(p.AtName("group_id"), "Invalid Attribute Combination", "group_id must be omitted unless mode is actor_group.")
		}
	}
}

func criteriaForCreate(ctx context.Context, model resourceResourceModel) (json.RawMessage, fwDiagnostics) {
	if model.Type.ValueString() != resourceTypeDevicePool {
		return nil, nil
	}
	if model.DeviceMembershipCriteria.IsNull() || model.DeviceMembershipCriteria.IsUnknown() {
		return emptyListedCriteria, nil
	}
	return criteriaToAPI(ctx, model.DeviceMembershipCriteria)
}

func criteriaToAPI(ctx context.Context, value types.Object) (json.RawMessage, fwDiagnostics) {
	var diags fwDiagnostics
	var model deviceMembershipCriteriaModel
	diags.Append(value.As(ctx, &model, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, diags
	}
	source, field, op := "device", "id", "eq"
	var ruleValue any
	switch model.Mode.ValueString() {
	case "listed":
		ids := []string{}
		diags.Append(model.DeviceIDs.ElementsAs(ctx, &ids, false)...)
		if ids == nil {
			ids = []string{}
		}
		op, ruleValue = "in", ids
	case "own_devices":
		field, ruleValue = "actor_id", map[string]string{"subject": "actor_id"}
	case "all_devices":
		field, ruleValue = "account_id", map[string]string{"subject": "account_id"}
	case "actor_group":
		source, ruleValue = "actor_group", model.GroupID.ValueString()
	default:
		diags.AddError("Invalid Device Membership Mode", fmt.Sprintf("Unknown mode %q.", model.Mode.ValueString()))
		return nil, diags
	}
	raw, err := json.Marshal(map[string]any{source: map[string]any{"field": field, "op": op, "value": ruleValue}})
	if err != nil {
		diags.AddError("Error Encoding Device Membership Criteria", err.Error())
	}
	return raw, diags
}

func criteriaFromAPI(ctx context.Context, raw json.RawMessage, prior types.Object) (types.Object, fwDiagnostics) {
	var diags fwDiagnostics
	null := types.ObjectNull(criteriaAttributeTypes)
	if len(raw) == 0 || string(raw) == "null" {
		return null, diags
	}
	var rules map[string]struct {
		Field string          `json:"field"`
		Op    string          `json:"op"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		diags.AddError("Error Reading Device Membership Criteria", err.Error())
		return null, diags
	}
	model := deviceMembershipCriteriaModel{DeviceIDs: types.SetNull(types.StringType), GroupID: types.StringNull()}
	var err error
	if rule, ok := rules["device"]; ok && len(rules) == 1 {
		switch {
		case rule.Field == "id" && rule.Op == "in":
			model.Mode = types.StringValue("listed")
			var ids []string
			err = json.Unmarshal(rule.Value, &ids)
			// Preserve omitted versus explicitly empty sets for configured criteria.
			keepNull := true
			if !prior.IsNull() && !prior.IsUnknown() {
				var old deviceMembershipCriteriaModel
				diags.Append(prior.As(ctx, &old, basetypes.ObjectAsOptions{})...)
				keepNull = old.DeviceIDs.IsNull()
			}
			if len(ids) > 0 || !keepNull {
				var setDiags fwDiagnostics
				model.DeviceIDs, setDiags = types.SetValueFrom(ctx, types.StringType, ids)
				diags.Append(setDiags...)
			}
		case (rule.Field == "actor_id" || rule.Field == "account_id") && rule.Op == "eq":
			var subject map[string]string
			err = json.Unmarshal(rule.Value, &subject)
			if err == nil && (len(subject) != 1 || subject["subject"] != rule.Field) {
				err = fmt.Errorf("unsupported subject rule")
			}
			mode := "own_devices"
			if rule.Field == "account_id" {
				mode = "all_devices"
			}
			model.Mode = types.StringValue(mode)
		default:
			err = fmt.Errorf("unsupported device rule")
		}
	} else if rule, ok := rules["actor_group"]; ok && len(rules) == 1 && rule.Field == "id" && rule.Op == "eq" {
		model.Mode = types.StringValue("actor_group")
		var id string
		err = json.Unmarshal(rule.Value, &id)
		model.GroupID = types.StringValue(id)
	} else {
		err = fmt.Errorf("unsupported membership criteria")
	}
	if err != nil {
		diags.AddError("Error Reading Device Membership Criteria", err.Error())
		return null, diags
	}
	value, valueDiags := types.ObjectValueFrom(ctx, criteriaAttributeTypes, model)
	diags.Append(valueDiags...)
	return value, diags
}
