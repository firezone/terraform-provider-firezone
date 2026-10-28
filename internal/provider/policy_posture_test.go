package provider

import (
	"context"
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

// pObj builds an HCL object literal from key, value pairs.
func pObj(t *testing.T, kv ...any) attr.Value {
	t.Helper()
	attrs := map[string]attr.Value{}
	for i := 0; i < len(kv); i += 2 {
		attrs[kv[i].(string)] = kv[i+1].(attr.Value)
	}
	v, err := postureObjectValue(attrs)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// pList builds an HCL tuple literal, the type [ ... ] has in config.
func pList(t *testing.T, items ...attr.Value) attr.Value {
	t.Helper()
	v, err := postureTupleValue(items)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func pStrs(t *testing.T, items ...string) attr.Value {
	t.Helper()
	values := make([]attr.Value, 0, len(items))
	for _, s := range items {
		values = append(values, types.StringValue(s))
	}
	return pList(t, values...)
}

func pNum(f float64) attr.Value { return types.NumberValue(big.NewFloat(f)) }

func pCheck(t *testing.T, field, op string, operand attr.Value, extra ...any) attr.Value {
	t.Helper()
	return pObj(t, append([]any{"field", types.StringValue(field), op, operand}, extra...)...)
}

func pValue(v attr.Value) postureValue { return postureValue{DynamicValue: types.DynamicValue(v)} }

func mustNode(t *testing.T, v attr.Value) firezone.PostureNode {
	t.Helper()
	node, diags := postureFromValue(pValue(v))
	if diags.HasError() || node == nil {
		t.Fatalf("postureFromValue: node %v, diags %v", node, diags)
	}
	return *node
}

func TestPostureFromValue(t *testing.T) {
	yes := types.BoolValue(true)
	value := pObj(t, "all", pList(t,
		pCheck(t, "intune.enrolled", "is", yes),
		pCheck(t, "firezone.last_seen_version", "gte", types.StringValue("@latest")),
		pCheck(t, "defender.os_build", "lt", pNum(40)),
		pCheck(t, "iru.device_capacity_gb", "gte", pNum(0.5)),
		pObj(t, "any", pList(t,
			pCheck(t, "intune.compliance_state", "is_in", pStrs(t, "compliant", "inGracePeriod"), "rows", types.StringValue("all")),
			pCheck(t, "iru.tags", "exists", yes),
		)),
		pObj(t, "not", pCheck(t, "sentinelone.infected", "is", yes)),
	))

	node := mustNode(t, value)
	got, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"and":[` +
		`{"field":"intune.enrolled","op":"is","value":true},` +
		`{"field":"firezone.last_seen_version","op":"gte","value":"@latest"},` +
		`{"field":"defender.os_build","op":"lt","value":40},` +
		`{"field":"iru.device_capacity_gb","op":"gte","value":0.5},` +
		`{"or":[{"field":"intune.compliance_state","op":"is_in","rows":"all","value":["compliant","inGracePeriod"]},` +
		`{"field":"iru.tags","op":"exists"}]},` +
		`{"not":{"field":"sentinelone.infected","op":"is","value":true}}]}`
	if string(got) != want {
		t.Errorf("wire form\n got: %s\nwant: %s", got, want)
	}

	// The API decodes numbers as float64 and lists as []any. Read
	// writes the tree back exactly as the config above spells it.
	var decoded firezone.PostureNode
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	for name, n := range map[string]firezone.PostureNode{"built": node, "from api": decoded} {
		back, err := postureToValue(&n)
		if err != nil {
			t.Fatalf("%s: postureToValue: %v", name, err)
		}
		if !back.Equal(pValue(value)) {
			t.Errorf("%s: read back differs\n got: %v\nwant: %v", name, back, value)
		}
	}
}

func TestPostureRootCheck(t *testing.T) {
	node := mustNode(t, pCheck(t, "intune.enrolled", "is", types.BoolValue(true)))
	if node.Field != "intune.enrolled" || node.And != nil {
		t.Errorf("want a bare check at the root, got %+v", node)
	}
}

func TestPostureNullKeysAreAbsent(t *testing.T) {
	// rows = var.strict ? "all" : null must mean no rows.
	node := mustNode(t, pCheck(t, "intune.enrolled", "is", types.BoolValue(true), "rows", types.StringNull()))
	if node.Rows != "" {
		t.Errorf("rows = %q, want unset", node.Rows)
	}
}

func TestPostureToValueNil(t *testing.T) {
	v, err := postureToValue(nil)
	if err != nil || !v.IsNull() {
		t.Fatalf("want null, got %v (%v)", v, err)
	}
}

func TestPostureEqual(t *testing.T) {
	yes := types.BoolValue(true)
	base := pObj(t, "all", pList(t,
		pCheck(t, "intune.compliance_state", "is_in", pStrs(t, "compliant")),
		pCheck(t, "defender.os_build", "gt", pNum(40)),
	))
	stringList, _ := types.ListValueFrom(context.Background(), types.StringType, []string{"compliant"})
	checkMap := types.MapValueMust(types.DynamicType, map[string]attr.Value{
		"field": types.DynamicValue(types.StringValue("defender.os_build")),
		"gt":    types.DynamicValue(pNum(40)),
	})

	same := map[string]attr.Value{
		"rows = any spelled out": pObj(t, "all", pList(t,
			pCheck(t, "intune.compliance_state", "is_in", pStrs(t, "compliant"), "rows", types.StringValue("any")),
			pCheck(t, "defender.os_build", "gt", pNum(40)),
		)),
		"list instead of tuple": pObj(t, "all", pList(t,
			pCheck(t, "intune.compliance_state", "is_in", stringList),
			pCheck(t, "defender.os_build", "gt", pNum(40)),
		)),
		"map instead of object": pObj(t, "all", pList(t,
			pCheck(t, "intune.compliance_state", "is_in", pStrs(t, "compliant")),
			checkMap,
		)),
	}
	for name, v := range same {
		if !postureEqual(pValue(base), pValue(v)) {
			t.Errorf("%s: want equal", name)
		}
	}

	different := map[string]attr.Value{
		"other value":    pObj(t, "all", pList(t, pCheck(t, "intune.compliance_state", "is_in", pStrs(t, "compliant")), pCheck(t, "defender.os_build", "gt", pNum(41)))),
		"other operator": pObj(t, "any", pList(t, pCheck(t, "intune.compliance_state", "is_in", pStrs(t, "compliant")), pCheck(t, "defender.os_build", "gt", pNum(40)))),
		"other order":    pObj(t, "all", pList(t, pCheck(t, "defender.os_build", "gt", pNum(40)), pCheck(t, "intune.compliance_state", "is_in", pStrs(t, "compliant")))),
		"rows all":       pObj(t, "all", pList(t, pCheck(t, "intune.compliance_state", "is_in", pStrs(t, "compliant"), "rows", types.StringValue("all")), pCheck(t, "defender.os_build", "gt", pNum(40)))),
		"negated":        pObj(t, "not", base),
		"other check":    pCheck(t, "intune.enrolled", "is", yes),
	}
	for name, v := range different {
		if postureEqual(pValue(base), pValue(v)) {
			t.Errorf("%s: want different", name)
		}
	}

	null := postureValue{DynamicValue: types.DynamicNull()}
	if !postureEqual(null, null) || postureEqual(null, pValue(base)) || postureEqual(pValue(base), null) {
		t.Error("null is equal only to null")
	}

	equal, diags := pValue(base).DynamicSemanticEquals(context.Background(), pValue(same["rows = any spelled out"]))
	if !equal || diags.HasError() {
		t.Errorf("DynamicSemanticEquals: %v, %v", equal, diags)
	}
}

func TestPostureValidation(t *testing.T) {
	str := func(s string) attr.Value { return types.StringValue(s) }
	yes := types.BoolValue(true)
	tests := []struct {
		name  string
		value attr.Value
		want  string
	}{
		{"not an object", str("intune.enrolled"), "expected an object"},
		{"no keys", pObj(t), "exactly one of all, any or not"},
		{"two combinators", pObj(t, "all", pList(t, pCheck(t, "intune.enrolled", "is", yes)), "any", pList(t, pCheck(t, "intune.enrolled", "is", yes))), "got all, any"},
		{"misspelled combinator", pObj(t, "and", pList(t, pCheck(t, "intune.enrolled", "is", yes))), "got and"},
		{"empty all", pObj(t, "all", pList(t)), "posture.all: needs at least one item"},
		{"all not a list", pObj(t, "all", pCheck(t, "intune.enrolled", "is", yes)), "expected a list"},
		{"set", pObj(t, "all", types.SetValueMust(types.StringType, []attr.Value{str("a")})), "tolist()"},
		{"location in message", pObj(t, "all", pList(t, pCheck(t, "intune.enrolled", "is", yes), pObj(t, "not", pCheck(t, "intune.enrolled", "gt", pNum(1))))), "posture.all[1].not.gt:"},
		{"unknown field", pCheck(t, "intune.brand_new_field", "is", yes), "not a device posture field"},
		{"field not a string", pObj(t, "field", pNum(1), "is", yes), "expected a string"},
		{"no operator", pObj(t, "field", str("intune.enrolled")), "exactly one operator"},
		{"two operators", pCheck(t, "defender.os_build", "gt", pNum(1), "lt", pNum(5)), "got gt, lt"},
		{"operator wrong for kind", pCheck(t, "intune.enrolled", "gt", pNum(1)), "not an operator for boolean field"},
		{"no value operator false", pCheck(t, "iru.tags", "exists", types.BoolValue(false)), "write exists = true"},
		{"list operator scalar", pCheck(t, "intune.compliance_state", "is_in", str("compliant")), "expected a list"},
		{"list operator empty", pCheck(t, "intune.compliance_state", "is_in", pStrs(t)), "1 to 100 strings"},
		{"list operator non-string", pCheck(t, "intune.compliance_state", "is_in", pList(t, pNum(1))), "is_in[0]: expected a string"},
		{"quoted bool", pCheck(t, "intune.enrolled", "is", str("true")), "Write true without quotes"},
		{"quoted number", pCheck(t, "defender.os_build", "gt", str("40")), "Write 40 without quotes"},
		{"fractional integer", pCheck(t, "defender.os_build", "gt", pNum(4.5)), "whole number"},
		{"unquoted version", pCheck(t, "intune.os_version", "gte", pNum(14.4)), "Quote versions"},
		{"bad rows", pCheck(t, "intune.enrolled", "is", yes, "rows", str("some")), "\"any\" or \"all\""},
		{"rows on firezone", pCheck(t, "firezone.last_seen_version", "exists", yes, "rows", str("all")), "exactly one device"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, diags := postureFromValue(pValue(tt.value))
			if !diags.HasError() || node != nil {
				t.Fatalf("want an error and no node, got %v / %v", node, diags)
			}
			if detail := diags.Errors()[0].Detail(); !strings.Contains(detail, tt.want) {
				t.Errorf("got %q, want it to contain %q", detail, tt.want)
			}
		})
	}
}

func TestPostureLimits(t *testing.T) {
	leaf := pCheck(t, "intune.enrolled", "exists", types.BoolValue(true))

	t.Run("too many checks", func(t *testing.T) {
		checks := make([]attr.Value, postureMaxLeaves+1)
		for i := range checks {
			checks[i] = leaf
		}
		_, diags := postureFromValue(pValue(pObj(t, "all", pList(t, checks...))))
		if !diags.HasError() || diags.Errors()[0].Summary() != "Too Many Posture Checks" {
			t.Fatalf("want a check limit error, got %v", diags)
		}
	})

	nest := func(levels int) attr.Value {
		v := leaf
		for range levels {
			v = pObj(t, "not", v)
		}
		return v
	}
	t.Run("depth", func(t *testing.T) {
		// Ten levels of all/any/not put the check at depth 10: allowed.
		if _, diags := postureFromValue(pValue(nest(postureMaxDepth))); diags.HasError() {
			t.Errorf("%d levels: %v", postureMaxDepth, diags)
		}
		_, diags := postureFromValue(pValue(nest(postureMaxDepth + 1)))
		if !diags.HasError() || diags.Errors()[0].Summary() != "Posture Nested Too Deeply" || len(diags.Errors()) != 1 {
			t.Errorf("%d levels: want one depth error, got %v", postureMaxDepth+1, diags)
		}
	})
}

func TestPostureUnknownDefers(t *testing.T) {
	for name, v := range map[string]postureValue{
		"whole value":   {DynamicValue: types.DynamicUnknown()},
		"operand":       pValue(pCheck(t, "intune.enrolled", "is", types.BoolUnknown())),
		"dynamic value": pValue(pCheck(t, "intune.enrolled", "is", types.DynamicUnknown())),
		"list":          pValue(pObj(t, "any", types.ListUnknown(types.DynamicType))),
		"field":         pValue(pObj(t, "field", types.StringUnknown(), "is", types.BoolValue(true))),
	} {
		node, diags := postureFromValue(v)
		if diags.HasError() || node != nil {
			t.Errorf("%s: want no node and no error, got %v / %v", name, node, diags)
		}
	}
}

func TestPostureKindsAndOperators(t *testing.T) {
	for field, kind := range postureFieldKinds {
		if len(postureKindOperators[kind]) == 0 {
			t.Errorf("%s: kind %v has no operators", field, kind)
		}
		provider, _, _ := strings.Cut(field, ".")
		if !slices.Contains(postureProviders, provider) {
			t.Errorf("%s: provider %q is not in postureProviders", field, provider)
		}
	}
	// Every operator is one some kind takes.
	for _, op := range slices.Concat(postureNoValueOperators, postureListOperators, postureScalarOperators) {
		found := op == "exists" || op == "does_not_exist"
		for _, ops := range postureKindOperators {
			found = found || slices.Contains(ops, op)
		}
		if !found {
			t.Errorf("operator %q is not taken by any kind", op)
		}
	}
}
