package provider

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-go"
)

// TestNullableString pins the three states an Optional string attribute
// maps onto in an update request, checking the encoded JSON rather than
// the struct: the whole point of the mapping is what reaches the wire.
//
// The null case is the one that matters. Update requests are
// merge-patch, so omitting the field keeps the server's old value and
// sending "" is ignored outright - only an explicit null clears it, and
// anything else leaves the read-back contradicting the plan.
func TestNullableString(t *testing.T) {
	tests := []struct {
		name  string
		input types.String
		want  string
	}{
		{"set", types.StringValue("prod database"), `{"v":"prod database"}`},
		{"empty string is sent as-is", types.StringValue(""), `{"v":""}`},
		{"null clears", types.StringNull(), `{"v":null}`},
		{"unknown is omitted", types.StringUnknown(), `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := struct {
				V *firezone.Null[string] `json:"v,omitempty"`
			}{V: nullableString(tt.input)}

			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(encoded) != tt.want {
				t.Errorf("got %s, want %s", encoded, tt.want)
			}
		})
	}
}

// TestIPStackForUpdate pins ip_stack's deliberate exception to that
// rule: it is never cleared, because the API rejects the field on any
// Resource type other than dns.
func TestIPStackForUpdate(t *testing.T) {
	tests := []struct {
		name  string
		input types.String
		want  string
	}{
		{"set", types.StringValue("ipv4_only"), `{"v":"ipv4_only"}`},
		{"null is omitted, not cleared", types.StringNull(), `{}`},
		{"unknown is omitted", types.StringUnknown(), `{}`},
		{"empty is omitted", types.StringValue(""), `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := struct {
				V *firezone.Null[firezone.IPStack] `json:"v,omitempty"`
			}{V: ipStackForUpdate(tt.input)}

			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(encoded) != tt.want {
				t.Errorf("got %s, want %s", encoded, tt.want)
			}
		})
	}
}
