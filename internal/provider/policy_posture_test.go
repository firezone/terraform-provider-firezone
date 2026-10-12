package provider

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

func postureCheckObj(t *testing.T, field, op string, value attr.Value, values []string, rows string) attr.Value {
	t.Helper()
	list := types.ListNull(types.StringType)
	if values != nil {
		list, _ = types.ListValueFrom(context.Background(), types.StringType, values)
	}
	dyn := types.StringNull()
	if value != nil {
		dyn = value.(types.String)
	}
	return types.ObjectValueMust(postureCheckAttrTypes, map[string]attr.Value{
		"field":    types.StringValue(field),
		"operator": types.StringValue(op),
		"value":    dyn,
		"values":   list,
		"rows":     types.StringValue(rows),
	})
}

// postureGroupObj builds a group with levels levels of room.
func postureGroupObj(op string, negate bool, levels int, checks, groups []attr.Value) attr.Value {
	attrs := map[string]attr.Value{
		"operator": types.StringValue(op),
		"negate":   types.BoolValue(negate),
		"check":    postureListOrNull(types.ObjectType{AttrTypes: postureCheckAttrTypes}, checks),
	}
	if levels > 1 {
		attrs["group"] = postureListOrNull(types.ObjectType{AttrTypes: postureGroupAttrTypes(levels - 1)}, groups)
	}
	return types.ObjectValueMust(postureGroupAttrTypes(levels), attrs)
}

func postureRoot(group attr.Value) types.List {
	return types.ListValueMust(types.ObjectType{AttrTypes: postureGroupAttrTypes(postureGroupLevels)}, []attr.Value{group})
}

func TestPostureFromList(t *testing.T) {
	enrolled := postureCheckObj(t, "intune.enrolled", "is", types.StringValue("true"), nil, "any")
	compliant := postureCheckObj(t, "intune.compliance_state", "is_in", nil, []string{"compliant", "inGracePeriod"}, "all")
	version := postureCheckObj(t, "firezone.last_seen_version", "gte", types.StringValue("@latest"), nil, "any")
	score := postureCheckObj(t, "defender.os_build", "lt", types.StringValue("40"), nil, "any")
	exists := postureCheckObj(t, "iru.tags", "exists", nil, nil, "any")

	leaf := func(g attr.Value) []attr.Value { return []attr.Value{g} }
	inner := postureGroupObj("or", false, postureGroupLevels-1, []attr.Value{compliant, exists}, nil)

	root := postureGroupObj("and", false, postureGroupLevels, []attr.Value{enrolled, version, score}, leaf(inner))
	node, diags := postureFromList(postureRoot(root))
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"and":[` +
		`{"field":"intune.enrolled","op":"is","value":true},` +
		`{"field":"firezone.last_seen_version","op":"gte","value":"@latest"},` +
		`{"field":"defender.os_build","op":"lt","value":40},` +
		`{"or":[{"field":"intune.compliance_state","op":"is_in","rows":"all","value":["compliant","inGracePeriod"]},` +
		`{"field":"iru.tags","op":"exists"}]}]}`
	if string(got) != want {
		t.Errorf("wire form\n got: %s\nwant: %s", got, want)
	}

	// The same tree read back from the API is the block it came from.
	back, diags := postureToList(node)
	if diags.HasError() {
		t.Fatalf("postureToList: %v", diags)
	}
	// The API decodes numbers as float64 and lists as []any.
	var decoded firezone.PostureNode
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	fromAPI, diags := postureToList(&decoded)
	if diags.HasError() {
		t.Fatalf("postureToList(api): %v", diags)
	}
	if want := postureRoot(root); !want.Equal(back) || !want.Equal(fromAPI) {
		t.Errorf("round trip differs\nwant: %v\nback: %v\napi:  %v", want, back, fromAPI)
	}
}

func TestPostureNegatedGroupRoundTrip(t *testing.T) {
	enrolled := postureCheckObj(t, "intune.enrolled", "is", types.StringValue("true"), nil, "any")
	root := postureGroupObj("or", true, postureGroupLevels, []attr.Value{enrolled}, nil)

	node, diags := postureFromList(postureRoot(root))
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if node.Not == nil || len(node.Not.Or) != 1 {
		t.Fatalf("want not(or(...)), got %+v", node)
	}
	back, _ := postureToList(node)
	if !postureRoot(root).Equal(back) {
		t.Errorf("round trip differs: %v", back)
	}
}

func TestPostureToListNil(t *testing.T) {
	list, diags := postureToList(nil)
	if diags.HasError() || !list.IsNull() {
		t.Fatalf("want null list, got %v (%v)", list, diags)
	}
}

func TestPostureToListUnsupported(t *testing.T) {
	check := firezone.PostureCheck("intune.enrolled", firezone.PostureOperatorIs, true)
	doubleNot := firezone.PostureNot(firezone.PostureNot(check))
	if _, diags := postureToList(&doubleNot); !diags.HasError() {
		t.Error("want an error for a negated negation")
	}
}

func TestPostureValidation(t *testing.T) {
	str := func(s string) attr.Value { return types.StringValue(s) }
	tests := []struct {
		name  string
		check attr.Value
		want  string
	}{
		{"no value operator with value", postureCheckObj(t, "intune.enrolled", "exists", types.StringValue("true"), nil, "any"), "takes no value"},
		{"list operator with value", postureCheckObj(t, "intune.compliance_state", "is_in", str("x"), nil, "any"), "use values instead of value"},
		{"list operator missing values", postureCheckObj(t, "intune.compliance_state", "is_in", nil, nil, "any"), "needs values"},
		{"empty values", postureCheckObj(t, "intune.compliance_state", "is_in", nil, []string{}, "any"), "1 to 100 items"},
		{"scalar with values", postureCheckObj(t, "intune.compliance_state", "is", nil, []string{"a"}, "any"), "use value instead of values"},
		{"scalar missing value", postureCheckObj(t, "intune.compliance_state", "is", nil, nil, "any"), "needs a value"},
		{"operator wrong for kind", postureCheckObj(t, "intune.enrolled", "gt", str("1"), nil, "any"), "does not apply to boolean field"},
		{"non-canonical bool", postureCheckObj(t, "intune.enrolled", "is", str("True"), nil, "any"), "\"true\" or \"false\""},
		{"non-integer", postureCheckObj(t, "defender.os_build", "gt", str("4.5"), nil, "any"), "whole number"},
		{"leading zero integer", postureCheckObj(t, "defender.os_build", "gt", str("040"), nil, "any"), "whole number"},
		{"rows on firezone", postureCheckObj(t, "firezone.last_seen_version", "exists", nil, nil, "all"), "exactly one device"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := postureGroupObj("and", false, postureGroupLevels, []attr.Value{tt.check}, nil)
			_, diags := postureFromList(postureRoot(root))
			if !diags.HasError() {
				t.Fatal("want an error")
			}
			if !strings.Contains(diags.Errors()[0].Detail(), tt.want) {
				t.Errorf("got %q, want it to contain %q", diags.Errors()[0].Detail(), tt.want)
			}
		})
	}

	t.Run("empty group", func(t *testing.T) {
		_, diags := postureFromList(postureRoot(postureGroupObj("and", false, postureGroupLevels, nil, nil)))
		if !diags.HasError() {
			t.Fatal("want an error")
		}
	})

	t.Run("too many leaves", func(t *testing.T) {
		var checks []attr.Value
		for range postureMaxLeaves + 1 {
			checks = append(checks, postureCheckObj(t, "intune.enrolled", "exists", nil, nil, "any"))
		}
		_, diags := postureFromList(postureRoot(postureGroupObj("and", false, postureGroupLevels, checks, nil)))
		if !diags.HasError() || !strings.Contains(diags.Errors()[0].Summary(), "Too Many") {
			t.Fatalf("want a leaf limit error, got %v", diags)
		}
	})

	t.Run("negation counts twice", func(t *testing.T) {
		// Ten levels of group fit only without negation: each negated
		// group costs a second API level.
		leafCheck := postureCheckObj(t, "intune.enrolled", "exists", nil, nil, "any")
		build := func(negate bool) types.List {
			g := postureGroupObj("and", negate, 1, []attr.Value{leafCheck}, nil)
			for levels := 2; levels <= postureGroupLevels; levels++ {
				g = postureGroupObj("and", negate, levels, nil, []attr.Value{g})
			}
			return postureRoot(g)
		}
		if _, diags := postureFromList(build(false)); diags.HasError() {
			// 10 plain groups put the check at depth 10: allowed.
			t.Errorf("plain nesting: %v", diags)
		}
		if _, diags := postureFromList(build(true)); !diags.HasError() {
			t.Error("negated nesting: want a depth error")
		}
	})

	t.Run("unknown defers", func(t *testing.T) {
		unknownCheck := types.ObjectValueMust(postureCheckAttrTypes, map[string]attr.Value{
			"field":    types.StringValue("intune.enrolled"),
			"operator": types.StringValue("is"),
			"value":    types.StringUnknown(),
			"values":   types.ListNull(types.StringType),
			"rows":     types.StringValue("any"),
		})
		node, diags := postureFromList(postureRoot(postureGroupObj("and", false, postureGroupLevels, []attr.Value{unknownCheck}, nil)))
		if diags.HasError() || node != nil {
			t.Fatalf("want no node and no error, got %v / %v", node, diags)
		}
	})
}

func TestPostureUnknownFieldIsAnError(t *testing.T) {
	check := postureCheckObj(t, "intune.brand_new_field", "gt", types.StringValue("5"), nil, "any")
	_, diags := postureFromList(postureRoot(postureGroupObj("and", false, postureGroupLevels, []attr.Value{check}, nil)))
	if !diags.HasError() || diags.Errors()[0].Summary() != "Unknown Posture Field" {
		t.Fatalf("want an unknown field error, got %v", diags)
	}
}

func TestPostureKindsAndOperators(t *testing.T) {
	for field, kind := range postureFieldKinds {
		if len(postureKindOperators[kind]) == 0 {
			t.Errorf("%s: kind %v has no operators", field, kind)
		}
	}
	// Every operator the schema accepts is one some kind takes.
	for _, op := range slices.Concat(postureListOperators, postureScalarOperators) {
		found := false
		for _, ops := range postureKindOperators {
			found = found || slices.Contains(ops, op)
		}
		if !found {
			t.Errorf("operator %q is accepted by the schema but no kind takes it", op)
		}
	}
}

func TestPostureFloatIsCanonical(t *testing.T) {
	if v, err := postureScalar(postureKindFloat, "0.5"); err != nil || v != 0.5 {
		t.Errorf("0.5: %v, %v", v, err)
	}
	if _, err := postureScalar(postureKindFloat, "0.50"); err == nil {
		t.Error("0.50 should be rejected: it would read back as 0.5")
	}
	leaf := firezone.PostureCheck("x.y", firezone.PostureOperatorGt, 0.1)
	obj, err := postureCheckFromNode(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if v := obj.Attributes()["value"]; !v.Equal(types.StringValue("0.1")) {
		t.Errorf("0.1 read back as %v", v)
	}
}
