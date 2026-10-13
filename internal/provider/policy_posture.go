package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

// A device posture is a boolean expression the API stores as a tree of
// and / or / not nodes over field checks. Terraform blocks cannot
// recurse, but the API bounds the tree - Portal.Policies.Postures allows
// 10 levels of and/or/not and 100 leaves - so the schema nests a single
// "group" block that deep instead:
//
//	posture {                       # the root group
//	  operator = "and"
//	  check { field = "intune.enrolled" operator = "is" value = true }
//	  group {
//	    operator = "or"
//	    check { ... }
//	  }
//	}
//
// A group is an and/or node, optionally wrapped in a not by negate. A
// check is a leaf. Mirrors the limits in Portal.Policies.Postures; keep
// in sync with the API.
const (
	// postureMaxDepth is the deepest an and/or/not/leaf node may sit,
	// counting the root as 0.
	postureMaxDepth = 10
	// postureMaxLeaves is the most checks one Policy may carry.
	postureMaxLeaves = 100
	// postureMaxListItems is the most items a list operator's values may hold.
	postureMaxListItems = 100
	// postureGroupLevels is how many group blocks can nest. A group
	// sits at depth 0-9, since its children sit one level deeper and
	// the deepest permitted node is at depth 10.
	postureGroupLevels = postureMaxDepth
)

var (
	// postureFieldPattern is the "<provider>.<attribute>" shape of a
	// check's field; the generated postureFieldKinds table decides which
	// ones exist.
	postureFieldPattern = regexp.MustCompile(`^(firezone|intune|iru|defender|santa|sentinelone|sophos)\.[a-z][a-z0-9_]*$`)

	postureNoValueOperators = []string{"exists", "does_not_exist", "is_empty", "is_not_empty"}
	postureListOperators    = []string{
		"is_in", "is_not_in", "contains_any_of", "contains_all_of", "is_in_cidr", "is_not_in_cidr",
	}
	postureScalarOperators = []string{
		"is", "is_not", "contains", "does_not_contain", "starts_with", "ends_with",
		"matches", "does_not_match", "eq", "ne", "gt", "gte", "lt", "lte",
		"before", "after", "within_last", "not_within_last",
	}
)

// postureKind is the type of a posture field's attribute. Mirrors the
// SDK's PostureXField types, which carry the operators the API accepts for
// each.
type postureKind int

const (
	postureKindBool postureKind = iota
	postureKindString
	postureKindInteger
	postureKindFloat
	postureKindVersion
	postureKindTimestamp
	postureKindIP
	postureKindJSON
	postureKindStringArray
)

func (k postureKind) String() string {
	return [...]string{"boolean", "string", "integer", "float", "version", "timestamp", "IP address", "JSON", "string list"}[k]
}

// postureKindOperators are the operators each kind takes besides exists
// and does_not_exist, which every field takes. Keep in sync with the SDK's
// posture_fields.go.
var postureKindOperators = map[postureKind][]string{
	postureKindBool:        {"is"},
	postureKindString:      {"is", "is_not", "contains", "does_not_contain", "starts_with", "ends_with", "is_in", "is_not_in", "matches", "does_not_match"},
	postureKindInteger:     {"eq", "ne", "gt", "gte", "lt", "lte"},
	postureKindFloat:       {"eq", "ne", "gt", "gte", "lt", "lte"},
	postureKindVersion:     {"is", "is_not", "gt", "gte", "lt", "lte"},
	postureKindTimestamp:   {"before", "after", "within_last", "not_within_last"},
	postureKindIP:          {"is_in_cidr", "is_not_in_cidr"},
	postureKindJSON:        {"is_empty", "is_not_empty"},
	postureKindStringArray: {"contains", "does_not_contain", "contains_any_of", "contains_all_of", "is_empty", "is_not_empty"},
}

// postureCheckAttrTypes are the attribute types of a check block.
var postureCheckAttrTypes = map[string]attr.Type{
	"field":    types.StringType,
	"operator": types.StringType,
	"value":    types.StringType,
	"values":   types.ListType{ElemType: types.StringType},
	"rows":     types.StringType,
}

// postureGroupAttrTypes returns the attribute types of a group block that
// may still nest levels-1 further groups.
func postureGroupAttrTypes(levels int) map[string]attr.Type {
	attrTypes := map[string]attr.Type{
		"operator": types.StringType,
		"negate":   types.BoolType,
		"check":    types.ListType{ElemType: types.ObjectType{AttrTypes: postureCheckAttrTypes}},
	}
	if levels > 1 {
		attrTypes["group"] = types.ListType{ElemType: types.ObjectType{AttrTypes: postureGroupAttrTypes(levels - 1)}}
	}
	return attrTypes
}

// postureBlock is the schema of the policy's posture block.
func postureBlock() schema.ListNestedBlock {
	block := postureGroupBlock(postureGroupLevels)
	block.Description = "Device posture requirements: checks the connecting device must pass, in " +
		"addition to every condition, before this Policy grants access. The block is the root " +
		"group of an and/or expression; nest group blocks for deeper logic. Needs the account's " +
		"device_posture entitlement. Changing it revokes the Policy's active authorizations, so " +
		"affected connections reauthorize.\n\n" +
		"The API allows at most 10 levels of nesting (a negated group counts for two) and 100 checks."
	block.Validators = append(block.Validators, listvalidator.SizeAtMost(1))
	return block
}

// postureGroupBlock builds a group block that may nest levels-1 further
// groups beneath it.
func postureGroupBlock(levels int) schema.ListNestedBlock {
	blocks := map[string]schema.Block{
		"check": schema.ListNestedBlock{
			Description: "Compares one device attribute against a value.",
			NestedObject: schema.NestedBlockObject{
				Attributes: map[string]schema.Attribute{
					"field": schema.StringAttribute{
						Required: true,
						Description: "The attribute to test, as \"<provider>.<attribute>\", for example " +
							"\"intune.compliance_state\" or \"firezone.last_seen_version\". The provider is one of " +
							"firezone (the connecting device itself), intune, iru, defender, santa, sentinelone " +
							"or sophos. See the Firezone documentation for each provider's attributes.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(postureFieldPattern,
								"must be \"<provider>.<attribute>\" with a provider of firezone, intune, iru, defender, santa, sentinelone or sophos"),
						},
					},
					"operator": schema.StringAttribute{
						Required: true,
						Description: "The comparison. Which operators apply depends on the attribute's type " +
							"and the API rejects a mismatch. Every attribute takes exists and does_not_exist; " +
							"strings take is, is_not, is_in, is_not_in, contains, does_not_contain, starts_with, " +
							"ends_with, matches, does_not_match; booleans take is; integers and floats take eq, ne, " +
							"gt, gte, lt, lte; versions take is, is_not, gt, gte, lt, lte; timestamps take before, " +
							"after, within_last, not_within_last; IP addresses take is_in_cidr, is_not_in_cidr; " +
							"lists take contains_any_of, contains_all_of, is_empty, is_not_empty. " +
							"Note that a missing attribute fails every operator except does_not_exist, including " +
							"negative ones such as is_not.",
						Validators: []validator.String{
							stringvalidator.OneOf(slices.Concat(postureNoValueOperators, postureListOperators, postureScalarOperators)...),
						},
					},
					"value": schema.StringAttribute{
						Optional: true,
						Description: "The value to compare against, written as a string whatever the attribute's " +
							"type: \"true\" or \"false\" for a boolean, a number such as \"40\" or \"0.5\" for an " +
							"integer or float (written in its shortest form, so \"0.5\" and not \"0.50\"), a " +
							"version such as \"14.4.1\" for a version. For a timestamp, an ISO 8601 datetime with " +
							"before and after, or an ISO 8601 duration such as \"PT24H\" with within_last and " +
							"not_within_last. For matches and does_not_match, a regular expression. For " +
							"firezone.last_seen_version, \"@latest\" means the newest Client release for the device's " +
							"platform. Not allowed for the list operators or the operators that take no value.",
					},
					"values": schema.ListAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "The strings to compare against, for the list operators is_in, is_not_in, " +
							"contains_any_of, contains_all_of (1-100 strings) and is_in_cidr, is_not_in_cidr " +
							"(1-100 CIDRs). Not allowed for any other operator.",
					},
					"rows": schema.StringAttribute{
						Optional: true,
						Computed: true,
						Default:  stringdefault.StaticString("any"),
						Description: "How the check treats a device that matches more than one record of an " +
							"integration provider: \"any\" holds when any matching record passes, \"all\" only " +
							"when every one does. Defaults to \"any\". Not valid for firezone attributes, " +
							"which always describe exactly one device.",
						Validators: []validator.String{stringvalidator.OneOf("any", "all")},
					},
				},
			},
		},
	}
	if levels > 1 {
		blocks["group"] = postureGroupBlock(levels - 1)
		nested := blocks["group"].(schema.ListNestedBlock)
		nested.Description = "A nested group of checks, combined with the enclosing group's operator " +
			"like any other check."
		blocks["group"] = nested
	}

	return schema.ListNestedBlock{
		NestedObject: schema.NestedBlockObject{
			Attributes: map[string]schema.Attribute{
				"operator": schema.StringAttribute{
					Required: true,
					Description: "\"and\" holds when every check and group in this group holds; \"or\" " +
						"when at least one does.",
					Validators: []validator.String{stringvalidator.OneOf("and", "or")},
				},
				"negate": schema.BoolAttribute{
					Optional: true,
					Computed: true,
					Default:  booldefault.StaticBool(false),
					Description: "Invert the group: it holds when the and/or would not. Counts for two " +
						"levels of the API's nesting limit.",
				},
			},
			Blocks: blocks,
		},
	}
}

// postureBuilder converts the posture block into an SDK expression,
// reporting every problem it can see at plan time.
type postureBuilder struct {
	diags  fwDiagnostics
	leaves int
	// tooDeep stops the depth error repeating for every node below the
	// first one over the limit.
	tooDeep bool
	// incomplete is set when a value was unknown, so the expression
	// cannot be built yet and the leaf-count check is not meaningful.
	incomplete bool
}

// postureFromList converts a posture block to the SDK's expression. The
// node is nil when the block is absent or not yet fully known, or when
// diagnostics were added.
func postureFromList(list types.List) (node *firezone.PostureNode, diags fwDiagnostics) {
	if list.IsNull() || list.IsUnknown() || len(list.Elements()) == 0 {
		return nil, nil
	}

	b := &postureBuilder{}
	root, ok := b.group(list.Elements()[0], path.Root("posture").AtListIndex(0), 0)
	if b.leaves > postureMaxLeaves && !b.incomplete {
		b.diags.AddAttributeError(path.Root("posture"), "Too Many Posture Checks",
			fmt.Sprintf("a Policy allows at most %d checks across its posture; this one has %d.",
				postureMaxLeaves, b.leaves))
	}
	if !ok || b.diags.HasError() || b.incomplete {
		return nil, b.diags
	}
	return &root, b.diags
}

// group converts one group object. depth is the API depth of the node
// the group becomes.
func (b *postureBuilder) group(elem attr.Value, at path.Path, depth int) (firezone.PostureNode, bool) {
	obj, ok := elem.(types.Object)
	if !ok || obj.IsNull() || obj.IsUnknown() {
		b.incomplete = true
		return firezone.PostureNode{}, false
	}
	attrs := obj.Attributes()

	operator, _ := attrs["operator"].(types.String)
	negate, _ := attrs["negate"].(types.Bool)
	if operator.IsUnknown() || operator.IsNull() || negate.IsUnknown() {
		b.incomplete = true
	}

	// A negated group is a not around its and/or, one level deeper.
	innerDepth := depth
	if negate.ValueBool() {
		innerDepth++
	}
	b.checkDepth(innerDepth, at)

	var children []firezone.PostureNode
	if checks, ok := attrs["check"].(types.List); ok {
		if checks.IsUnknown() {
			b.incomplete = true
		}
		for i, c := range checks.Elements() {
			b.checkDepth(innerDepth+1, at.AtName("check").AtListIndex(i))
			if child, ok := b.check(c, at.AtName("check").AtListIndex(i)); ok {
				children = append(children, child)
			}
		}
	}
	if groups, ok := attrs["group"].(types.List); ok {
		if groups.IsUnknown() {
			b.incomplete = true
		}
		for i, g := range groups.Elements() {
			if child, ok := b.group(g, at.AtName("group").AtListIndex(i), innerDepth+1); ok {
				children = append(children, child)
			}
		}
	}

	if len(children) == 0 {
		if !b.incomplete {
			b.diags.AddAttributeError(at, "Empty Posture Group",
				"a group needs at least one check or group block.")
		}
		return firezone.PostureNode{}, false
	}
	if operator.IsUnknown() || operator.IsNull() || negate.IsUnknown() {
		return firezone.PostureNode{}, false
	}

	var node firezone.PostureNode
	if operator.ValueString() == "or" {
		node = firezone.PostureOr(children...)
	} else {
		node = firezone.PostureAnd(children...)
	}
	if negate.ValueBool() {
		node = firezone.PostureNot(node)
	}
	return node, true
}

// checkDepth reports a node deeper than the API allows, once.
func (b *postureBuilder) checkDepth(depth int, at path.Path) {
	if depth <= postureMaxDepth || b.tooDeep {
		return
	}
	b.tooDeep = true
	b.diags.AddAttributeError(at, "Posture Nested Too Deeply",
		fmt.Sprintf("the API allows at most %d levels of and/or/not above a check. A negated group "+
			"counts for two levels. Flatten the expression.", postureMaxDepth))
}

// check converts one check object and validates its operator/value pairing.
func (b *postureBuilder) check(elem attr.Value, at path.Path) (firezone.PostureNode, bool) {
	b.leaves++

	obj, ok := elem.(types.Object)
	if !ok || obj.IsNull() || obj.IsUnknown() {
		b.incomplete = true
		return firezone.PostureNode{}, false
	}
	attrs := obj.Attributes()

	field, _ := attrs["field"].(types.String)
	operator, _ := attrs["operator"].(types.String)
	value, _ := attrs["value"].(types.String)
	values, _ := attrs["values"].(types.List)
	rows, _ := attrs["rows"].(types.String)

	if field.IsUnknown() || operator.IsUnknown() || value.IsUnknown() || values.IsUnknown() || rows.IsUnknown() ||
		field.IsNull() || operator.IsNull() {
		b.incomplete = true
		return firezone.PostureNode{}, false
	}

	if rows.ValueString() == "all" && strings.HasPrefix(field.ValueString(), "firezone.") {
		b.diags.AddAttributeError(at.AtName("rows"), "Invalid Posture Check",
			fmt.Sprintf("rows does not apply to %q, which describes exactly one device.", field.ValueString()))
	}

	op := operator.ValueString()
	kind, known := postureFieldKinds[field.ValueString()]
	if !known {
		b.diags.AddAttributeError(at.AtName("field"), "Unknown Posture Field",
			fmt.Sprintf("%q is not a device posture field. Fields are \"<provider>.<attribute>\", such as "+
				"\"intune.compliance_state\"; a field newer than this provider needs a provider upgrade.", field.ValueString()))
		return firezone.PostureNode{}, false
	}
	if !slices.Contains(postureKindOperators[kind], op) && op != "exists" && op != "does_not_exist" {
		valid := append(slices.Clone(postureKindOperators[kind]), "exists", "does_not_exist")
		b.diags.AddAttributeError(at.AtName("operator"), "Invalid Posture Check",
			fmt.Sprintf("operator %q does not apply to %s field %q. Valid operators: %s.",
				op, kind, field.ValueString(), strings.Join(valid, ", ")))
	}

	hasValue := !value.IsNull()
	hasValues := !values.IsNull()

	var wire any
	switch {
	case slices.Contains(postureNoValueOperators, op):
		if hasValue || hasValues {
			b.diags.AddAttributeError(at.AtName("operator"), "Invalid Posture Check",
				fmt.Sprintf("operator %q takes no value; remove value and values.", op))
		}
	case slices.Contains(postureListOperators, op):
		if hasValue {
			b.diags.AddAttributeError(at.AtName("value"), "Invalid Posture Check",
				fmt.Sprintf("operator %q takes a list; use values instead of value.", op))
		}
		if !hasValues {
			b.diags.AddAttributeError(at.AtName("operator"), "Invalid Posture Check",
				fmt.Sprintf("operator %q needs values.", op))
			break
		}
		items := make([]string, 0, len(values.Elements()))
		for _, e := range values.Elements() {
			s, ok := e.(types.String)
			if !ok || s.IsUnknown() || s.IsNull() {
				b.incomplete = true
				continue
			}
			items = append(items, s.ValueString())
		}
		if n := len(values.Elements()); n == 0 || n > postureMaxListItems {
			b.diags.AddAttributeError(at.AtName("values"), "Invalid Posture Check",
				fmt.Sprintf("values must hold 1 to %d items; got %d.", postureMaxListItems, n))
		}
		wire = items
	default:
		if hasValues {
			b.diags.AddAttributeError(at.AtName("values"), "Invalid Posture Check",
				fmt.Sprintf("operator %q takes a single value; use value instead of values.", op))
		}
		if !hasValue {
			b.diags.AddAttributeError(at.AtName("operator"), "Invalid Posture Check",
				fmt.Sprintf("operator %q needs a value.", op))
			break
		}
		scalar, err := postureScalar(kind, value.ValueString())
		if err != nil {
			b.diags.AddAttributeError(at.AtName("value"), "Invalid Posture Check", err.Error())
			break
		}
		wire = scalar
	}

	node := firezone.PostureCheck(field.ValueString(), firezone.PostureOperator(op), wire)
	if rows.ValueString() == "all" {
		node = node.WithRows(firezone.PostureRowsAll)
	}
	return node, true
}

// postureScalar converts a check's string value to the JSON scalar the
// API takes for kind. Numbers and booleans must be written in their
// canonical form, as that is how they read back and a different spelling
// of the same value would show as a change on every plan.
func postureScalar(kind postureKind, value string) (any, error) {
	switch kind {
	case postureKindBool:
		if value != "true" && value != "false" {
			return nil, fmt.Errorf("value must be \"true\" or \"false\" for a boolean field; got %q", value)
		}
		return value == "true", nil
	case postureKindInteger:
		i, err := strconv.ParseInt(value, 10, 64)
		if err != nil || strconv.FormatInt(i, 10) != value {
			return nil, fmt.Errorf("value must be a whole number such as \"40\" for an integer field; got %q", value)
		}
		return i, nil
	case postureKindFloat:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || strconv.FormatFloat(f, 'f', -1, 64) != value {
			return nil, fmt.Errorf("value must be a number in its shortest form, such as \"0.5\", for a float field; got %q", value)
		}
		return f, nil
	}
	return value, nil
}

// postureToList converts an SDK expression to the posture block value.
// A nil expression is a null list.
func postureToList(node *firezone.PostureNode) (types.List, fwDiagnostics) {
	var diags fwDiagnostics
	elemType := types.ObjectType{AttrTypes: postureGroupAttrTypes(postureGroupLevels)}
	if node == nil {
		return types.ListNull(elemType), diags
	}

	group, err := postureGroupFromNode(*node, postureGroupLevels)
	if err != nil {
		diags.AddError("Unsupported Device Posture",
			"The Policy's device posture cannot be written as firezone_policy posture blocks: "+err.Error()+
				" Change the posture in the portal, or manage it with a shape the provider supports.")
		return types.ListNull(elemType), diags
	}

	list, d := types.ListValue(elemType, []attr.Value{group})
	diags.Append(d...)
	return list, diags
}

// postureGroupFromNode converts an and/or/not node to a group object
// that may nest levels-1 further groups. A bare check or a negated check
// - both of which a group can only hold as a child - is wrapped in an and
// group, which means the same thing.
func postureGroupFromNode(node firezone.PostureNode, levels int) (types.Object, error) {
	if levels < 1 {
		return types.ObjectNull(nil), fmt.Errorf("it nests deeper than the provider's %d groups", postureGroupLevels)
	}

	negate := false
	if node.Not != nil {
		inner := *node.Not
		if inner.And == nil && inner.Or == nil {
			if inner.Not != nil {
				return types.ObjectNull(nil), fmt.Errorf("it negates a negation")
			}
			// not(check): a negated group holding only the check.
			return postureBuildGroup("and", true, []firezone.PostureNode{inner}, levels)
		}
		negate = true
		node = inner
	}

	switch {
	case node.And != nil:
		return postureBuildGroup("and", negate, node.And, levels)
	case node.Or != nil:
		return postureBuildGroup("or", negate, node.Or, levels)
	default:
		// A bare check at the root.
		return postureBuildGroup("and", false, []firezone.PostureNode{node}, levels)
	}
}

func postureBuildGroup(operator string, negate bool, children []firezone.PostureNode, levels int) (types.Object, error) {
	checkType := types.ObjectType{AttrTypes: postureCheckAttrTypes}
	groupType := types.ObjectType{AttrTypes: postureGroupAttrTypes(levels - 1)}

	var checks, groups []attr.Value
	for _, child := range children {
		if child.And == nil && child.Or == nil && child.Not == nil {
			check, err := postureCheckFromNode(child)
			if err != nil {
				return types.ObjectNull(nil), err
			}
			checks = append(checks, check)
			continue
		}
		group, err := postureGroupFromNode(child, levels-1)
		if err != nil {
			return types.ObjectNull(nil), err
		}
		groups = append(groups, group)
	}

	attrs := map[string]attr.Value{
		"operator": types.StringValue(operator),
		"negate":   types.BoolValue(negate),
		"check":    postureListOrNull(checkType, checks),
	}
	if levels > 1 {
		attrs["group"] = postureListOrNull(groupType, groups)
	} else if len(groups) > 0 {
		return types.ObjectNull(nil), fmt.Errorf("it nests deeper than the provider's %d groups", postureGroupLevels)
	}

	obj, diags := types.ObjectValue(postureGroupAttrTypes(levels), attrs)
	if diags.HasError() {
		return types.ObjectNull(nil), fmt.Errorf("internal error building a group: %s", diags[0].Detail())
	}
	return obj, nil
}

func postureListOrNull(elemType attr.Type, elems []attr.Value) types.List {
	if len(elems) == 0 {
		return types.ListNull(elemType)
	}
	return types.ListValueMust(elemType, elems)
}

// postureCheckFromNode converts a leaf to a check object.
func postureCheckFromNode(leaf firezone.PostureNode) (types.Object, error) {
	value := types.StringNull()
	values := types.ListNull(types.StringType)

	switch v := leaf.Value.(type) {
	case nil:
	case string:
		value = types.StringValue(v)
	case bool:
		value = types.StringValue(strconv.FormatBool(v))
	case float64:
		value = types.StringValue(strconv.FormatFloat(v, 'f', -1, 64))
	case int64:
		value = types.StringValue(strconv.FormatInt(v, 10))
	case []string:
		list, d := types.ListValueFrom(context.Background(), types.StringType, v)
		if d.HasError() {
			return types.ObjectNull(nil), fmt.Errorf("%s has unreadable values", leaf.Field)
		}
		values = list
	case []any:
		items := make([]attr.Value, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return types.ObjectNull(nil), fmt.Errorf("%s has a non-string list item", leaf.Field)
			}
			items = append(items, types.StringValue(s))
		}
		values = types.ListValueMust(types.StringType, items)
	default:
		return types.ObjectNull(nil), fmt.Errorf("%s has a value of unexpected type %T", leaf.Field, v)
	}

	rows := "any"
	if leaf.Rows == firezone.PostureRowsAll {
		rows = "all"
	}

	obj, diags := types.ObjectValue(postureCheckAttrTypes, map[string]attr.Value{
		"field":    types.StringValue(leaf.Field),
		"operator": types.StringValue(string(leaf.Operator)),
		"value":    value,
		"values":   values,
		"rows":     types.StringValue(rows),
	})
	if diags.HasError() {
		return types.ObjectNull(nil), fmt.Errorf("internal error building a check: %s", diags[0].Detail())
	}
	return obj, nil
}
