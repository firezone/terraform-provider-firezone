package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	firezone "github.com/firezone/firezone-go"
)

func TestAccPolicyResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyResourceConfig("eng access"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "description", "eng access"),
					resource.TestCheckResourceAttr("firezone_policy.test", "condition.0.property", "remote_ip_location_region"),
					resource.TestCheckResourceAttr("firezone_policy.test", "condition.0.values.0", "US"),
					// Second condition block: covers both multi-condition
					// policies (the API ANDs them) and the
					// current_utc_datetime property, whose values have a
					// bespoke DAY/TIME_RANGES/TIMEZONE format.
					resource.TestCheckResourceAttr("firezone_policy.test", "condition.1.property", "current_utc_datetime"),
					resource.TestCheckResourceAttr("firezone_policy.test", "condition.1.operator", "is_in_day_of_week_time_ranges"),
					resource.TestCheckResourceAttr("firezone_policy.test", "condition.1.values.0", "M/09:00-17:00/America/New_York"),
				),
			},
			{
				ResourceName:      "firezone_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccPolicyResourceConfig("eng access, updated"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "description", "eng access, updated"),
				),
			},
		},
	})
}

func testAccPolicyResourceConfig(description string) string {
	return `
resource "firezone_site" "test" {
  name = "acc-test-site"
}

resource "firezone_resource" "test" {
  site_id = firezone_site.test.id
  name    = "acc-test-resource"
  type    = "dns"
  address = "internal.example.com"
}

resource "firezone_group" "test" {
  name = "acc-test-group"
}

resource "firezone_policy" "test" {
  group_id    = firezone_group.test.id
  resource_id = firezone_resource.test.id
  description = "` + description + `"

  condition {
    property = "remote_ip_location_region"
    operator = "is_in"
    values   = ["US", "CA"]
  }

  condition {
    property = "current_utc_datetime"
    operator = "is_in_day_of_week_time_ranges"
    values = [
      "M/09:00-17:00/America/New_York",
      "T/09:00-17:00/America/New_York",
    ]
  }
}
`
}

// TestPolicyModelFromAPI_DescriptionNullVsEmpty pins the null-vs-empty
// handling for a Policy's description.
//
// The API has no empty description, only a null one, and returns it as
// "". A config that omitted the argument planned null, so echoing ""
// back into state is an inconsistent-result error on the very first
// apply of a Policy without a description.
func TestPolicyModelFromAPI_DescriptionNullVsEmpty(t *testing.T) {
	tests := []struct {
		name       string
		apiValue   string
		wantNull   bool
		wantString string
	}{
		{name: "empty reads back as null", apiValue: "", wantNull: true},
		{name: "set reads back as its value", apiValue: "prod access", wantString: "prod access"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var model policyResourceModel
			diags := policyModelFromAPI(context.Background(), &firezone.Policy{Description: tt.apiValue}, &model)
			if diags.HasError() {
				t.Fatalf("policyModelFromAPI returned diagnostics: %v", diags)
			}

			if got := model.Description.IsNull(); got != tt.wantNull {
				t.Fatalf("Description.IsNull() = %v, want %v", got, tt.wantNull)
			}
			if !tt.wantNull && model.Description.ValueString() != tt.wantString {
				t.Errorf("Description = %q, want %q", model.Description.ValueString(), tt.wantString)
			}
		})
	}
}
