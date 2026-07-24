package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
}
`
}
