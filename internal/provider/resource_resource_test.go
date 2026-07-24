package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccResourceResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceResourceConfig("postgres-prod", "10.0.1.0/24"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_resource.test", "name", "postgres-prod"),
					resource.TestCheckResourceAttr("firezone_resource.test", "type", "cidr"),
					resource.TestCheckResourceAttr("firezone_resource.test", "filters.0.protocol", "tcp"),
					resource.TestCheckResourceAttr("firezone_resource.test", "filters.0.ports.0", "5432"),
				),
			},
			{
				ResourceName:      "firezone_resource.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccResourceResourceConfig("postgres-prod-renamed", "10.0.1.0/24"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_resource.test", "name", "postgres-prod-renamed"),
				),
			},
		},
	})
}

func testAccResourceResourceConfig(name, address string) string {
	return fmt.Sprintf(`
resource "firezone_site" "test" {
  name = "acc-test-site"
}

resource "firezone_resource" "test" {
  site_id = firezone_site.test.id
  name    = %q
  type    = "cidr"
  address = %q

  filters {
    protocol = "tcp"
    ports    = ["5432"]
  }
}
`, name, address)
}
