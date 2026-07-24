package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccGatewayResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGatewayResourceConfig("gw-nyc-1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_gateway.test", "name", "gw-nyc-1"),
					resource.TestCheckResourceAttrSet("firezone_gateway.test", "token"),
				),
			},
			// "token" is excluded from verification: the API can never
			// return an existing Gateway's token, so ImportState always
			// leaves it empty (see resource_gateway.go) - that's
			// expected behavior, not a bug, so every other field is
			// still verified normally.
			{
				ResourceName:            "firezone_gateway.test",
				ImportState:             true,
				ImportStateIdFunc:       testAccGatewayImportStateID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"token"},
			},
			{
				Config: testAccGatewayResourceConfig("gw-nyc-1-renamed"),
				Check:  resource.TestCheckResourceAttr("firezone_gateway.test", "name", "gw-nyc-1-renamed"),
			},
		},
	})
}

func testAccGatewayResourceConfig(name string) string {
	return fmt.Sprintf(`
resource "firezone_site" "test" {
  name = "acc-test-site"
}

resource "firezone_gateway" "test" {
  site_id = firezone_site.test.id
  name    = %q
}
`, name)
}

func testAccGatewayImportStateID(s *terraform.State) (string, error) {
	rs, ok := s.RootModule().Resources["firezone_gateway.test"]
	if !ok {
		return "", fmt.Errorf("firezone_gateway.test not found in state")
	}
	return rs.Primary.Attributes["site_id"] + "/" + rs.Primary.ID, nil
}
