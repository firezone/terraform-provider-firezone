package provider

import (
	"context"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	firezone "github.com/firezone/firezone-sdk-go"
)

func TestAccPolicyResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyResourceConfig("eng access", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "description", "eng access"),
					resource.TestCheckResourceAttr("firezone_policy.test", "enabled", "true"),
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
				Config: testAccPolicyResourceConfig("eng access, updated", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "description", "eng access, updated"),
				),
			},
			{
				Config: testAccPolicyResourceConfig("eng access, updated", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "enabled", "false"),
				),
			},
			{
				Config: testAccPolicyResourceConfig("eng access, updated", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "enabled", "true"),
				),
			},
		},
	})
}

// TestAccPolicyResource_CreateDisabled covers creating a Policy that is
// disabled from the start, which the API takes on create - there is no
// window where it grants access before a follow-up disable.
func TestAccPolicyResource_CreateDisabled(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyResourceConfig("disabled from the start", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "enabled", "false"),
				),
			},
			{
				ResourceName:      "firezone_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccPolicyResourceConfig(description string, enabled bool) string {
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
  enabled     = ` + strconv.FormatBool(enabled) + `

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

// TestPolicyModelFromAPI_Enabled pins that enabled is the inverse of the
// API's is_disabled.
func TestPolicyModelFromAPI_Enabled(t *testing.T) {
	tests := []struct {
		name       string
		isDisabled bool
		want       bool
	}{
		{name: "enabled policy", isDisabled: false, want: true},
		{name: "disabled policy", isDisabled: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var model policyResourceModel
			diags := policyModelFromAPI(context.Background(), &firezone.Policy{IsDisabled: tt.isDisabled}, &model)
			if diags.HasError() {
				t.Fatalf("policyModelFromAPI returned diagnostics: %v", diags)
			}

			if got := model.Enabled.ValueBool(); got != tt.want {
				t.Errorf("Enabled = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPolicyModelFromAPI_RequireAttestation pins that require_attestation
// is read back from the API, so a Policy whose certificate requirement
// was changed outside Terraform shows a diff on the next plan.
func TestPolicyModelFromAPI_RequireAttestation(t *testing.T) {
	for _, want := range []bool{true, false} {
		var model policyResourceModel
		diags := policyModelFromAPI(context.Background(), &firezone.Policy{RequireAttestation: want}, &model)
		if diags.HasError() {
			t.Fatalf("policyModelFromAPI returned diagnostics: %v", diags)
		}

		if got := model.RequireAttestation.ValueBool(); got != want {
			t.Errorf("RequireAttestation = %v, want %v", got, want)
		}
	}
}

// TestAccPolicyResource_Posture covers device posture blocks: create with a
// nested expression, import, change a check, then remove the posture.
// Needs an account with the device_posture entitlement.
func TestAccPolicyResource_Posture(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPolicyPostureConfig("compliant"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.0.operator", "and"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.0.negate", "false"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.0.check.0.field", "intune.enrolled"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.0.check.0.value", "true"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.0.group.0.operator", "or"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.0.group.0.check.0.values.0", "compliant"),
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.0.group.0.check.0.rows", "all"),
				),
			},
			{
				ResourceName:      "firezone_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccPolicyPostureConfig("inGracePeriod"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_policy.test", "posture.0.group.0.check.0.values.0", "inGracePeriod"),
				),
			},
			{
				Config: testAccPolicyResourceConfig("no posture", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("firezone_policy.test", "posture.0.operator"),
				),
			},
		},
	})
}

func testAccPolicyPostureConfig(complianceState string) string {
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

  posture {
    operator = "and"

    check {
      field    = "intune.enrolled"
      operator = "is"
      value    = "true"
    }

    group {
      operator = "or"

      check {
        field    = "intune.compliance_state"
        operator = "is_in"
        values   = ["` + complianceState + `"]
        rows     = "all"
      }

      check {
        field    = "firezone.last_seen_version"
        operator = "gte"
        value    = "@latest"
      }
    }
  }
}
`
}
