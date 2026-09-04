package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

// stringList builds a non-null list. values must be non-nil: a nil
// slice round-trips through ListValueFrom as a *null* list, which is
// the very distinction these tests exist to check.
func stringList(t *testing.T, values []string) types.List {
	t.Helper()
	if values == nil {
		t.Fatal("stringList: values must be non-nil; use types.ListNull for a null list")
	}
	list, diags := types.ListValueFrom(context.Background(), types.StringType, values)
	if diags.HasError() {
		t.Fatalf("ListValueFrom returned diagnostics: %v", diags)
	}
	return list
}

// TestFiltersToModel_PortsNullVsEmpty pins the null-vs-empty handling
// for a filter's ports.
//
// The API always sends a ports array, using [] for filters that have
// none (icmp). Terraform treats a null ports argument and an explicit
// ports = [] as different values, so a naive echo of [] turns an
// omitted argument into "Provider produced inconsistent result after
// apply".
func TestFiltersToModel_PortsNullVsEmpty(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		apiPorts []string
		prior    []resourceFilterModel
		wantNull bool
		wantLen  int
	}{
		{
			// The reported bug: an icmp filter with no ports argument.
			name:     "omitted ports stay null",
			apiPorts: []string{},
			prior:    []resourceFilterModel{{Ports: types.ListNull(types.StringType)}},
			wantNull: true,
		},
		{
			// The mirror image: an explicit empty list must not silently
			// become null, or the next plan shows a phantom diff.
			name:     "explicit empty list stays empty",
			apiPorts: []string{},
			prior:    []resourceFilterModel{{Ports: stringList(t, []string{})}},
			wantNull: false,
			wantLen:  0,
		},
		{
			name:     "no prior defaults to null",
			apiPorts: []string{},
			prior:    nil,
			wantNull: true,
		},
		{
			name:     "real ports are returned as-is",
			apiPorts: []string{"80", "8000 - 9000"},
			prior:    []resourceFilterModel{{Ports: types.ListNull(types.StringType)}},
			wantNull: false,
			wantLen:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filters := []firezone.Filter{{Protocol: firezone.FilterProtocolICMP, Ports: tt.apiPorts}}

			got, diags := filtersToModel(ctx, filters, tt.prior)
			if diags.HasError() {
				t.Fatalf("filtersToModel returned diagnostics: %v", diags)
			}
			if len(got) != 1 {
				t.Fatalf("len(got) = %d, want 1", len(got))
			}

			if got[0].Ports.IsNull() != tt.wantNull {
				t.Fatalf("Ports.IsNull() = %v, want %v (ports: %v)",
					got[0].Ports.IsNull(), tt.wantNull, got[0].Ports)
			}
			if !tt.wantNull && len(got[0].Ports.Elements()) != tt.wantLen {
				t.Errorf("len(Ports) = %d, want %d", len(got[0].Ports.Elements()), tt.wantLen)
			}
		})
	}
}

// TestFiltersToModel_MixedFilters covers the exact shape from the
// provider example: filters carrying ports alongside an icmp filter
// that has none, with prior values only for some of them.
func TestFiltersToModel_MixedFilters(t *testing.T) {
	ctx := context.Background()

	filters := []firezone.Filter{
		{Protocol: firezone.FilterProtocolTCP, Ports: []string{"80", "443"}},
		{Protocol: firezone.FilterProtocolUDP, Ports: []string{"51820"}},
		{Protocol: firezone.FilterProtocolICMP, Ports: []string{}},
	}
	prior := []resourceFilterModel{
		{Ports: stringList(t, []string{"80", "443"})},
		{Ports: stringList(t, []string{"51820"})},
		{Ports: types.ListNull(types.StringType)},
	}

	got, diags := filtersToModel(ctx, filters, prior)
	if diags.HasError() {
		t.Fatalf("filtersToModel returned diagnostics: %v", diags)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}

	if got[0].Protocol.ValueString() != "tcp" || len(got[0].Ports.Elements()) != 2 {
		t.Errorf("filters[0] = %+v, want tcp with 2 ports", got[0])
	}
	if got[1].Protocol.ValueString() != "udp" || len(got[1].Ports.Elements()) != 1 {
		t.Errorf("filters[1] = %+v, want udp with 1 port", got[1])
	}
	if !got[2].Ports.IsNull() {
		t.Errorf("filters[2].Ports = %v, want null", got[2].Ports)
	}
}

// TestResourceModelFromAPI_NullableStrings covers the Optional string
// attributes the API omits for Resource types that don't have them.
// Each must read back as null rather than "", or Terraform rejects the
// apply as an inconsistent result.
func TestResourceModelFromAPI_NullableStrings(t *testing.T) {
	ctx := context.Background()

	// A static_device_pool: no address and no site, both nulled by the
	// API regardless of what was sent.
	pool := &firezone.Resource{
		ID:   "res-1",
		Name: "field-laptops",
		Type: firezone.ResourceTypeStaticDevicePool,
	}

	var model resourceResourceModel
	if diags := resourceModelFromAPI(ctx, pool, &model); diags.HasError() {
		t.Fatalf("resourceModelFromAPI returned diagnostics: %v", diags)
	}

	for _, tc := range []struct {
		name  string
		value types.String
	}{
		{"address", model.Address},
		{"address_description", model.AddressDescription},
		{"ip_stack", model.IPStack},
		{"site_id", model.SiteID},
	} {
		if !tc.value.IsNull() {
			t.Errorf("%s = %v, want null", tc.name, tc.value)
		}
	}
}
