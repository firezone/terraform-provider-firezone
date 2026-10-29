package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	firezone "github.com/firezone/firezone-sdk-go"
)

// A device posture is a boolean expression the API stores as a tree of
// and / or / not nodes over field checks. Terraform schemas cannot
// recurse, so the posture attribute is dynamic and the provider reads
// the tree out of plain HCL values:
//
//	posture = {
//	  all = [
//	    { field = "intune.enrolled", is = true },
//	    { any = [
//	      { field = "intune.compliance_state", is_in = ["compliant"], rows = "all" },
//	      { field = "firezone.last_seen_version", gte = "@latest" },
//	    ] },
//	    { not = { field = "sentinelone.infected", is = true } },
//	  ]
//	}
//
// An object with "all", "any" or "not" is an and, or or not node. An
// object with "field" is a check: its one other key, besides an optional
// "rows", names the API operator and holds its operand. Every API tree
// has exactly one such spelling, which is what Read writes back, so an
// imported posture reads as the config that would produce it.
//
// Limits mirror Portal.Policies.Postures; keep in sync with the API.
const (
	// postureMaxDepth is the deepest an and/or/not/leaf node may sit,
	// counting the root as 0.
	postureMaxDepth = 10
	// postureMaxLeaves is the most checks one Policy may carry.
	postureMaxLeaves = 100
	// postureMaxListItems is the most items a list operator's values may hold.
	postureMaxListItems = 100
)

// The keys of a posture object, other than the operator names.
const (
	postureKeyAll   = "all"
	postureKeyAny   = "any"
	postureKeyNot   = "not"
	postureKeyField = "field"
	postureKeyRows  = "rows"
)

var (
	postureProviders = []string{"firezone", "intune", "iru", "defender", "santa", "sentinelone", "sophos"}

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

// postureOperatorsFor lists every operator kind takes, exists and
// does_not_exist included.
func postureOperatorsFor(kind postureKind) []string {
	return append(slices.Clone(postureKindOperators[kind]), "exists", "does_not_exist")
}

// postureAttribute is the schema of the policy's posture attribute.
func postureAttribute() schema.DynamicAttribute {
	return schema.DynamicAttribute{
		Optional:   true,
		CustomType: postureType{},
		Description: "Device posture requirements: checks the connecting device must pass, in addition to every " +
			"condition, before this Policy grants access. Needs the account's device_posture entitlement. " +
			"Changing it revokes the Policy's active authorizations, so affected connections reauthorize.\n\n" +
			"The value is an expression built from HCL objects. Each object is exactly one of:\n\n" +
			"- `{ all = [ ... ] }`: holds when every item holds.\n" +
			"- `{ any = [ ... ] }`: holds when at least one item holds.\n" +
			"- `{ not = { ... } }`: holds when its item does not.\n" +
			"- `{ field = \"<provider>.<attribute>\", <operator> = <value> }`: a check of one device attribute. " +
			"The provider is firezone (the connecting device itself), intune, iru, defender, santa, sentinelone " +
			"or sophos. A check may also set `rows = \"all\"`, so that it holds only when every record the " +
			"integration provider has for the device passes; the default, `\"any\"`, needs one. rows does not " +
			"apply to firezone attributes.\n\n" +
			"The value's type follows the attribute's: `true` or `false` for a boolean, a number for an integer " +
			"or float, a string for a string, version or timestamp, and a list of strings for the list " +
			"operators. Operators by attribute type:\n\n" +
			"- boolean: `is`\n" +
			"- string: `is`, `is_not`, `contains`, `does_not_contain`, `starts_with`, `ends_with`, " +
			"`matches`, `does_not_match` (regular expressions), `is_in`, `is_not_in` (lists). Comparison ignores case.\n" +
			"- integer, float: `eq`, `ne`, `gt`, `gte`, `lt`, `lte`\n" +
			"- version: `is`, `is_not`, `gt`, `gte`, `lt`, `lte`, with a dotted version such as `\"14.4.1\"`. " +
			"For firezone.last_seen_version, `\"@latest\"` means the newest Client release for the device's platform.\n" +
			"- timestamp: `before`, `after` with an ISO 8601 datetime; `within_last`, `not_within_last` with an " +
			"ISO 8601 duration such as `\"PT24H\"` or `\"P30D\"`\n" +
			"- IP address: `is_in_cidr`, `is_not_in_cidr`, with a list of CIDRs\n" +
			"- string list: `contains`, `does_not_contain` with a string; `contains_any_of`, `contains_all_of` " +
			"with a list; `is_empty`, `is_not_empty`\n" +
			"- JSON: `is_empty`, `is_not_empty`\n" +
			"- every attribute: `exists`, `does_not_exist`\n\n" +
			"Operators that take no value are written `= true`, for example `{ field = \"iru.serial_number\", " +
			"exists = true }`. A missing attribute fails every operator except does_not_exist, including " +
			"negative ones such as is_not.\n\n" +
			"The API allows at most 10 levels of all/any/not above a check and 100 checks. Importing a Policy " +
			"reads its posture back in this form.",
	}
}

// postureFromValue converts a posture value to the SDK's expression. The
// node is nil when the value is null or not yet fully known, or when
// diagnostics were added.
func postureFromValue(v postureValue) (*firezone.PostureNode, fwDiagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return nil, nil
	}
	p := &postureParser{}
	root, ok := p.node(v.DynamicValue, "posture", 0)
	if p.leaves > postureMaxLeaves && !p.incomplete {
		p.errorf("posture", "Too Many Posture Checks",
			"a Policy allows at most %d checks across its posture; this one has %d.", postureMaxLeaves, p.leaves)
	}
	if !ok || p.diags.HasError() || p.incomplete {
		return nil, p.diags
	}
	return &root, p.diags
}

// postureParser converts a posture value into an SDK expression,
// reporting every problem it can see at plan time.
type postureParser struct {
	diags  fwDiagnostics
	leaves int
	// tooDeep stops the depth error repeating for every node below the
	// first one over the limit.
	tooDeep bool
	// incomplete is set when a value was unknown, so the expression
	// cannot be built yet and the leaf-count check is not meaningful.
	incomplete bool
}

// errorf reports a problem at loc, a readable path such as
// "posture.all[1].not". Terraform cannot point inside a dynamic value, so
// the diagnostic names the attribute and the detail names the spot.
func (p *postureParser) errorf(loc, summary, format string, args ...any) {
	p.diags.AddAttributeError(path.Root("posture"), summary, loc+": "+fmt.Sprintf(format, args...))
}

// known unwraps v to the concrete value underneath any dynamic wrapper.
// It returns false, marking the parse incomplete, when v is unknown, and
// nil when it is null.
func (p *postureParser) known(v attr.Value) (attr.Value, bool) {
	for {
		if v == nil || v.IsNull() {
			return nil, true
		}
		if v.IsUnknown() {
			p.incomplete = true
			return nil, false
		}
		d, ok := v.(basetypes.DynamicValue)
		if !ok {
			return v, true
		}
		v = d.UnderlyingValue()
	}
}

// node converts one posture object. depth is the API depth of the node
// it becomes.
func (p *postureParser) node(v attr.Value, loc string, depth int) (firezone.PostureNode, bool) {
	v, ok := p.known(v)
	if !ok {
		return firezone.PostureNode{}, false
	}
	keys, isObject := postureObject(v)
	if !isObject {
		p.errorf(loc, "Invalid Posture", "expected an object such as { all = [...] } or "+
			"{ field = \"intune.enrolled\", is = true }; got %s.", postureDescribe(v))
		return firezone.PostureNode{}, false
	}
	if depth > postureMaxDepth && !p.tooDeep {
		p.tooDeep = true
		p.errorf(loc, "Posture Nested Too Deeply",
			"the API allows at most %d levels of all/any/not above a check. Flatten the expression.", postureMaxDepth)
	}

	if _, isCheck := keys[postureKeyField]; isCheck {
		return p.check(keys, loc)
	}

	names := slices.Sorted(maps.Keys(keys))
	if len(names) != 1 || !slices.Contains([]string{postureKeyAll, postureKeyAny, postureKeyNot}, names[0]) {
		p.errorf(loc, "Invalid Posture",
			"an object needs exactly one of all, any or not, or a field for a check; got %s.", postureKeyList(names))
		return firezone.PostureNode{}, false
	}

	switch key := names[0]; key {
	case postureKeyNot:
		child, ok := p.node(keys[key], loc+".not", depth+1)
		if !ok {
			return firezone.PostureNode{}, false
		}
		return firezone.PostureNot(child), true
	default:
		items, ok := p.list(keys[key], loc+"."+key)
		if !ok {
			return firezone.PostureNode{}, false
		}
		if len(items) == 0 {
			p.errorf(loc+"."+key, "Invalid Posture", "needs at least one item.")
			return firezone.PostureNode{}, false
		}
		children := make([]firezone.PostureNode, 0, len(items))
		valid := true
		for i, item := range items {
			child, ok := p.node(item, fmt.Sprintf("%s.%s[%d]", loc, key, i), depth+1)
			valid = valid && ok
			children = append(children, child)
		}
		if !valid {
			return firezone.PostureNode{}, false
		}
		if key == postureKeyAny {
			return firezone.PostureOr(children...), true
		}
		return firezone.PostureAnd(children...), true
	}
}

// check converts the keys of one check object and validates its
// operator and value against the field's type.
func (p *postureParser) check(keys map[string]attr.Value, loc string) (firezone.PostureNode, bool) {
	p.leaves++

	field, ok := p.string(keys[postureKeyField], loc+".field")
	if !ok {
		return firezone.PostureNode{}, false
	}
	kind, known := postureFieldKinds[field]
	if !known {
		p.errorf(loc+".field", "Unknown Posture Field",
			"%q is not a device posture field. Fields are \"<provider>.<attribute>\" with a provider of %s, "+
				"such as \"intune.compliance_state\"; a field newer than this provider needs a provider upgrade.",
			field, strings.Join(postureProviders, ", "))
		return firezone.PostureNode{}, false
	}

	var rows string
	if raw, set := keys[postureKeyRows]; set {
		if rows, ok = p.string(raw, loc+".rows"); !ok {
			return firezone.PostureNode{}, false
		}
		switch {
		case rows != "any" && rows != "all":
			p.errorf(loc+".rows", "Invalid Posture Check", "must be \"any\" or \"all\"; got %q.", rows)
		case rows == "all" && strings.HasPrefix(field, "firezone."):
			p.errorf(loc+".rows", "Invalid Posture Check",
				"rows does not apply to %q, which describes exactly one device.", field)
		}
	}

	var ops []string
	for key := range keys {
		if key != postureKeyField && key != postureKeyRows {
			ops = append(ops, key)
		}
	}
	sort.Strings(ops)
	valid := postureOperatorsFor(kind)
	if len(ops) != 1 {
		p.errorf(loc, "Invalid Posture Check",
			"a check needs exactly one operator besides field and rows; got %s. Operators for %s field %q: %s.",
			postureKeyList(ops), kind, field, strings.Join(valid, ", "))
		return firezone.PostureNode{}, false
	}
	op := ops[0]
	if !slices.Contains(valid, op) {
		p.errorf(loc+"."+op, "Invalid Posture Check",
			"%q is not an operator for %s field %q. Valid operators: %s.", op, kind, field, strings.Join(valid, ", "))
		return firezone.PostureNode{}, false
	}

	value, ok := p.operand(kind, op, keys[op], loc+"."+op)
	if !ok {
		return firezone.PostureNode{}, false
	}
	node := firezone.PostureCheck(field, firezone.PostureOperator(op), value)
	if rows == "all" {
		node = node.WithRows(firezone.PostureRowsAll)
	}
	return node, true
}

// operand converts an operator's value to the JSON the API takes for it.
func (p *postureParser) operand(kind postureKind, op string, raw attr.Value, loc string) (any, bool) {
	v, ok := p.known(raw)
	if !ok {
		return nil, false
	}

	switch {
	case slices.Contains(postureNoValueOperators, op):
		if b, isBool := v.(types.Bool); !isBool || !b.ValueBool() {
			p.errorf(loc, "Invalid Posture Check", "%s takes no value; write %s = true.", op, op)
			return nil, false
		}
		return nil, true

	case slices.Contains(postureListOperators, op):
		items, ok := p.list(v, loc)
		if !ok {
			return nil, false
		}
		if n := len(items); n == 0 || n > postureMaxListItems {
			p.errorf(loc, "Invalid Posture Check", "%s takes a list of 1 to %d strings; got %d.", op, postureMaxListItems, n)
			return nil, false
		}
		values := make([]string, 0, len(items))
		valid := true
		for i, item := range items {
			s, ok := p.string(item, fmt.Sprintf("%s[%d]", loc, i))
			valid = valid && ok
			values = append(values, s)
		}
		return values, valid
	}

	switch kind {
	case postureKindBool:
		if b, isBool := v.(types.Bool); isBool {
			return b.ValueBool(), true
		}
		p.errorf(loc, "Invalid Posture Check", "expected true or false for a boolean field; got %s.%s",
			postureDescribe(v), postureUnquoteHint(v))
	case postureKindInteger:
		if n, isNumber := v.(types.Number); isNumber {
			if i, accuracy := n.ValueBigFloat().Int64(); accuracy == big.Exact {
				return i, true
			}
		}
		p.errorf(loc, "Invalid Posture Check", "expected a whole number for an integer field; got %s.%s",
			postureDescribe(v), postureUnquoteHint(v))
	case postureKindFloat:
		if n, isNumber := v.(types.Number); isNumber {
			f, _ := n.ValueBigFloat().Float64()
			return f, true
		}
		p.errorf(loc, "Invalid Posture Check", "expected a number for a float field; got %s.%s",
			postureDescribe(v), postureUnquoteHint(v))
	default:
		if _, isString := v.(types.String); isString {
			return p.string(v, loc)
		}
		hint := ""
		if kind == postureKindVersion {
			hint = " Quote versions, for example \"14.4\", so that 14.4 and 14.40 stay different."
		}
		p.errorf(loc, "Invalid Posture Check", "%s takes a string for a %s field; got %s.%s",
			op, kind, postureDescribe(v), hint)
	}
	return nil, false
}

// string reads a known string.
func (p *postureParser) string(raw attr.Value, loc string) (string, bool) {
	v, ok := p.known(raw)
	if !ok {
		return "", false
	}
	s, isString := v.(types.String)
	if !isString {
		p.errorf(loc, "Invalid Posture", "expected a string; got %s.", postureDescribe(v))
		return "", false
	}
	return s.ValueString(), true
}

// list reads the items of a known list or tuple. Sets are refused: their
// order is not the config's, so they could not read back as written.
func (p *postureParser) list(raw attr.Value, loc string) ([]attr.Value, bool) {
	v, ok := p.known(raw)
	if !ok {
		return nil, false
	}
	switch l := v.(type) {
	case types.Tuple:
		return l.Elements(), true
	case types.List:
		return l.Elements(), true
	}
	p.errorf(loc, "Invalid Posture", "expected a list such as [ ... ]; got %s.", postureDescribe(v))
	return nil, false
}

// postureObject returns the keys of an object or map whose values are
// not null. A null key counts as absent, so a config can write
// rows = var.strict ? "all" : null.
func postureObject(v attr.Value) (map[string]attr.Value, bool) {
	var all map[string]attr.Value
	switch o := v.(type) {
	case types.Object:
		all = o.Attributes()
	case types.Map:
		all = o.Elements()
	default:
		return nil, false
	}
	keys := make(map[string]attr.Value, len(all))
	for k, e := range all {
		if !e.IsNull() {
			keys[k] = e
		}
	}
	return keys, true
}

// postureDescribe names the kind of value v is, for error messages.
func postureDescribe(v attr.Value) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case types.String:
		return strconv.Quote(t.ValueString())
	case types.Bool:
		return strconv.FormatBool(t.ValueBool())
	case types.Number:
		return t.ValueBigFloat().Text('g', -1)
	case types.Tuple, types.List:
		return "a list"
	case types.Set:
		return "a set (convert it with tolist())"
	case types.Object, types.Map:
		return "an object"
	}
	return v.String()
}

// postureUnquoteHint suggests dropping the quotes from a string that
// reads as a boolean or number, the form posture checks took before
// values were typed.
func postureUnquoteHint(v attr.Value) string {
	s, ok := v.(types.String)
	if !ok {
		return ""
	}
	if _, err := strconv.ParseFloat(s.ValueString(), 64); err == nil || s.ValueString() == "true" || s.ValueString() == "false" {
		return fmt.Sprintf(" Write %s without quotes.", s.ValueString())
	}
	return ""
}

func postureKeyList(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, ", ")
}

// postureToValue converts an SDK expression to its posture value: the
// one spelling of that tree, which is what an import reads back. A nil
// expression is null.
func postureToValue(node *firezone.PostureNode) (postureValue, error) {
	if node == nil {
		return postureValue{DynamicValue: types.DynamicNull()}, nil
	}
	v, err := postureNodeValue(*node)
	if err != nil {
		return postureValue{DynamicValue: types.DynamicNull()}, err
	}
	return postureValue{DynamicValue: types.DynamicValue(v)}, nil
}

func postureNodeValue(node firezone.PostureNode) (attr.Value, error) {
	switch {
	case node.Not != nil:
		child, err := postureNodeValue(*node.Not)
		if err != nil {
			return nil, err
		}
		return postureObjectValue(map[string]attr.Value{postureKeyNot: child})
	case node.And != nil, node.Or != nil:
		key, children := postureKeyAll, node.And
		if node.Or != nil {
			key, children = postureKeyAny, node.Or
		}
		items := make([]attr.Value, 0, len(children))
		for _, child := range children {
			item, err := postureNodeValue(child)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		list, err := postureTupleValue(items)
		if err != nil {
			return nil, err
		}
		return postureObjectValue(map[string]attr.Value{key: list})
	}

	operand, err := postureOperandValue(node.Value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", node.Field, err)
	}
	attrs := map[string]attr.Value{
		postureKeyField:       types.StringValue(node.Field),
		string(node.Operator): operand,
	}
	if node.Rows == firezone.PostureRowsAll {
		attrs[postureKeyRows] = types.StringValue(string(firezone.PostureRowsAll))
	}
	return postureObjectValue(attrs)
}

// postureOperandValue converts a check's value as the API returns it -
// numbers as float64 and lists as []any - to the HCL value a config
// writes for it.
func postureOperandValue(value any) (attr.Value, error) {
	switch v := value.(type) {
	case nil:
		return types.BoolValue(true), nil
	case string:
		return types.StringValue(v), nil
	case bool:
		return types.BoolValue(v), nil
	case float64:
		return types.NumberValue(big.NewFloat(v)), nil
	case int64:
		return types.NumberValue(new(big.Float).SetInt64(v)), nil
	case []string:
		items := make([]attr.Value, 0, len(v))
		for _, s := range v {
			items = append(items, types.StringValue(s))
		}
		return postureTupleValue(items)
	case []any:
		items := make([]attr.Value, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("a list holds a %T, not a string", item)
			}
			items = append(items, types.StringValue(s))
		}
		return postureTupleValue(items)
	}
	return nil, fmt.Errorf("unexpected value of type %T", value)
}

// postureObjectValue and postureTupleValue build the object and tuple
// types an HCL literal has, rather than a map or list, so that a value
// read back has the type the same config would.
func postureObjectValue(attrs map[string]attr.Value) (attr.Value, error) {
	attrTypes := make(map[string]attr.Type, len(attrs))
	for k, v := range attrs {
		attrTypes[k] = v.Type(context.Background())
	}
	obj, diags := types.ObjectValue(attrTypes, attrs)
	if diags.HasError() {
		return nil, fmt.Errorf("building an object: %s", diags[0].Detail())
	}
	return obj, nil
}

func postureTupleValue(items []attr.Value) (attr.Value, error) {
	elemTypes := make([]attr.Type, 0, len(items))
	for _, item := range items {
		elemTypes = append(elemTypes, item.Type(context.Background()))
	}
	tuple, diags := types.TupleValue(elemTypes, items)
	if diags.HasError() {
		return nil, fmt.Errorf("building a list: %s", diags[0].Detail())
	}
	return tuple, nil
}

// postureEqual reports whether two posture values are the same
// expression, however each is spelled: a list or a tuple, rows = "any"
// or no rows, 40 or 40.0. Both must be complete and valid.
func postureEqual(a, b postureValue) bool {
	if a.IsNull() || b.IsNull() {
		return a.IsNull() && b.IsNull()
	}
	nodeA, diagsA := postureFromValue(a)
	nodeB, diagsB := postureFromValue(b)
	if nodeA == nil || nodeB == nil || diagsA.HasError() || diagsB.HasError() {
		return false
	}
	return postureCanonicalJSON(*nodeA) == postureCanonicalJSON(*nodeB)
}

// postureCanonicalJSON is the wire form of node with every rows = "any"
// dropped, since the API reads an absent rows as "any".
func postureCanonicalJSON(node firezone.PostureNode) string {
	b, err := json.Marshal(postureDropAnyRows(node))
	if err != nil {
		return ""
	}
	return string(b)
}

func postureDropAnyRows(node firezone.PostureNode) firezone.PostureNode {
	if node.Rows == firezone.PostureRowsAny {
		node.Rows = ""
	}
	if node.Not != nil {
		child := postureDropAnyRows(*node.Not)
		node.Not = &child
	}
	for _, children := range []*[]firezone.PostureNode{&node.And, &node.Or} {
		if *children == nil {
			continue
		}
		mapped := make([]firezone.PostureNode, 0, len(*children))
		for _, child := range *children {
			mapped = append(mapped, postureDropAnyRows(child))
		}
		*children = mapped
	}
	return node
}

var (
	_ basetypes.DynamicTypable                    = postureType{}
	_ basetypes.DynamicValuableWithSemanticEquals = postureValue{}
)

// postureType is the dynamic type of the posture attribute. Its values
// compare by the expression they describe, so a config spelled
// differently from what Read returns - [...] for a list, rows = "any",
// 40.0 for 40 - is not reported as drift.
type postureType struct {
	basetypes.DynamicType
}

func (t postureType) Equal(o attr.Type) bool {
	_, ok := o.(postureType)
	return ok
}

func (t postureType) String() string { return "postureType" }

func (t postureType) ValueFromDynamic(_ context.Context, in basetypes.DynamicValue) (basetypes.DynamicValuable, diag.Diagnostics) {
	return postureValue{DynamicValue: in}, nil
}

func (t postureType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	v, err := t.DynamicType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	d, ok := v.(basetypes.DynamicValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", v)
	}
	return postureValue{DynamicValue: d}, nil
}

func (t postureType) ValueType(context.Context) attr.Value { return postureValue{} }

// postureValue is a posture attribute value.
type postureValue struct {
	basetypes.DynamicValue
}

func (v postureValue) Equal(o attr.Value) bool {
	other, ok := o.(postureValue)
	return ok && v.DynamicValue.Equal(other.DynamicValue)
}

func (v postureValue) Type(context.Context) attr.Type { return postureType{} }

// DynamicSemanticEquals lets the framework keep the config's spelling of
// a posture when the API returns the same expression in its own.
func (v postureValue) DynamicSemanticEquals(ctx context.Context, prior basetypes.DynamicValuable) (bool, diag.Diagnostics) {
	other, diags := prior.ToDynamicValue(ctx)
	if diags.HasError() {
		return false, diags
	}
	return postureEqual(v, postureValue{DynamicValue: other}), nil
}

// postureScalar converts a string value from an old posture block to the
// JSON scalar the API takes for kind. See policy_posture_v0.go.
func postureScalar(kind postureKind, value string) (any, error) {
	switch kind {
	case postureKindBool:
		if value != "true" && value != "false" {
			return nil, fmt.Errorf("value must be \"true\" or \"false\" for a boolean field; got %q", value)
		}
		return value == "true", nil
	case postureKindInteger:
		i, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value must be a whole number for an integer field; got %q", value)
		}
		return i, nil
	case postureKindFloat:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("value must be a number for a float field; got %q", value)
		}
		return f, nil
	}
	return value, nil
}
