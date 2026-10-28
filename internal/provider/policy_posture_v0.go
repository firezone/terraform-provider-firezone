package provider

import (
	"context"
	"fmt"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

// In 0.2.x and 0.3.x the posture was a nested "posture" block: a group with an
// operator ("and" or "or"), an optional negate, check blocks and group
// blocks nested 10 deep. Schema version 1 replaced it with the dynamic
// posture attribute, and upgradePolicyStateV0 rewrites old state into it.

// postureV0GroupLevels is how deep the old group blocks nested.
const postureV0GroupLevels = 10

// policyResourceModelV0 is firezone_policy state at schema version 0.
// State from 0.2.x has no require_attestation; it decodes as null and the
// next Read fills it in.
type policyResourceModelV0 struct {
	ID                    types.String           `tfsdk:"id"`
	GroupID               types.String           `tfsdk:"group_id"`
	ResourceID            types.String           `tfsdk:"resource_id"`
	Description           types.String           `tfsdk:"description"`
	FlowLogUploadsEnabled types.Bool             `tfsdk:"flow_log_uploads_enabled"`
	Enabled               types.Bool             `tfsdk:"enabled"`
	RequireAttestation    types.Bool             `tfsdk:"require_attestation"`
	Condition             []policyConditionModel `tfsdk:"condition"`
	Posture               types.List             `tfsdk:"posture"`
}

// policySchemaV0 is the version 0 schema: the current one with the posture
// block in place of the posture attribute. Only its types matter, as it
// is used to decode old state and never to validate config.
func policySchemaV0(current schema.Schema) schema.Schema {
	attrs := maps.Clone(current.Attributes)
	delete(attrs, "posture")
	blocks := maps.Clone(current.Blocks)
	blocks["posture"] = postureV0GroupBlock(postureV0GroupLevels)
	return schema.Schema{Attributes: attrs, Blocks: blocks}
}

func postureV0GroupBlock(levels int) schema.ListNestedBlock {
	blocks := map[string]schema.Block{
		"check": schema.ListNestedBlock{
			NestedObject: schema.NestedBlockObject{
				Attributes: map[string]schema.Attribute{
					"field":    schema.StringAttribute{Required: true},
					"operator": schema.StringAttribute{Required: true},
					"value":    schema.StringAttribute{Optional: true},
					"values":   schema.ListAttribute{Optional: true, ElementType: types.StringType},
					"rows":     schema.StringAttribute{Optional: true, Computed: true},
				},
			},
		},
	}
	if levels > 1 {
		blocks["group"] = postureV0GroupBlock(levels - 1)
	}
	return schema.ListNestedBlock{
		NestedObject: schema.NestedBlockObject{
			Attributes: map[string]schema.Attribute{
				"operator": schema.StringAttribute{Required: true},
				"negate":   schema.BoolAttribute{Optional: true, Computed: true},
			},
			Blocks: blocks,
		},
	}
}

// upgradePolicyStateV0 converts version 0 state, rewriting its posture block
// as the posture attribute's value.
func upgradePolicyStateV0(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	var old policyResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}

	posture := postureValue{DynamicValue: types.DynamicNull()}
	if !old.Posture.IsNull() && len(old.Posture.Elements()) > 0 {
		node, err := postureV0Group(old.Posture.Elements()[0])
		if err == nil {
			posture, err = postureToValue(&node)
		}
		if err != nil {
			resp.Diagnostics.AddError("Unable to Upgrade Policy State",
				"The Policy's posture block could not be converted to the posture attribute: "+err.Error()+
					" Remove the Policy from state and import it again.")
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, policyResourceModel{
		ID:                    old.ID,
		GroupID:               old.GroupID,
		ResourceID:            old.ResourceID,
		Description:           old.Description,
		FlowLogUploadsEnabled: old.FlowLogUploadsEnabled,
		Enabled:               old.Enabled,
		RequireAttestation:    old.RequireAttestation,
		Condition:             old.Condition,
		Posture:               posture,
	})...)
}

// postureV0Group converts an old group object to the expression it
// stood for. The state was valid when written, so this only checks
// what it needs to build the tree.
func postureV0Group(elem attr.Value) (firezone.PostureNode, error) {
	obj, ok := elem.(types.Object)
	if !ok || obj.IsNull() {
		return firezone.PostureNode{}, fmt.Errorf("a group is not an object")
	}
	attrs := obj.Attributes()

	var children []firezone.PostureNode
	if checks, ok := attrs["check"].(types.List); ok {
		for _, c := range checks.Elements() {
			child, err := postureV0Check(c)
			if err != nil {
				return firezone.PostureNode{}, err
			}
			children = append(children, child)
		}
	}
	if groups, ok := attrs["group"].(types.List); ok {
		for _, g := range groups.Elements() {
			child, err := postureV0Group(g)
			if err != nil {
				return firezone.PostureNode{}, err
			}
			children = append(children, child)
		}
	}
	if len(children) == 0 {
		return firezone.PostureNode{}, fmt.Errorf("a group has no checks")
	}

	operator, _ := attrs["operator"].(types.String)
	node := firezone.PostureAnd(children...)
	if operator.ValueString() == "or" {
		node = firezone.PostureOr(children...)
	}
	if negate, _ := attrs["negate"].(types.Bool); negate.ValueBool() {
		node = firezone.PostureNot(node)
	}
	return node, nil
}

func postureV0Check(elem attr.Value) (firezone.PostureNode, error) {
	obj, ok := elem.(types.Object)
	if !ok || obj.IsNull() {
		return firezone.PostureNode{}, fmt.Errorf("a check is not an object")
	}
	attrs := obj.Attributes()
	field, _ := attrs["field"].(types.String)
	operator, _ := attrs["operator"].(types.String)
	value, _ := attrs["value"].(types.String)
	values, _ := attrs["values"].(types.List)
	rows, _ := attrs["rows"].(types.String)

	var wire any
	switch {
	case !values.IsNull():
		items := make([]string, 0, len(values.Elements()))
		for _, e := range values.Elements() {
			s, _ := e.(types.String)
			items = append(items, s.ValueString())
		}
		wire = items
	case !value.IsNull():
		// A field this provider no longer knows keeps its string; the
		// next plan reports it.
		wire = value.ValueString()
		if kind, known := postureFieldKinds[field.ValueString()]; known {
			scalar, err := postureScalar(kind, value.ValueString())
			if err != nil {
				return firezone.PostureNode{}, fmt.Errorf("%s: %w", field.ValueString(), err)
			}
			wire = scalar
		}
	}

	node := firezone.PostureCheck(field.ValueString(), firezone.PostureOperator(operator.ValueString()), wire)
	if rows.ValueString() == "all" {
		node = node.WithRows(firezone.PostureRowsAll)
	}
	return node, nil
}
