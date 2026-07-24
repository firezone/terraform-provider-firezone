package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccSiteResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read.
			{
				Config: testAccSiteResourceConfig("primary-dc"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_site.test", "name", "primary-dc"),
					resource.TestCheckResourceAttrSet("firezone_site.test", "id"),
				),
			},
			// ImportState.
			{
				ResourceName:      "firezone_site.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Update and Read.
			{
				Config: testAccSiteResourceConfig("renamed-dc"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_site.test", "name", "renamed-dc"),
				),
			},
			// Delete happens automatically at the end of the test.
		},
	})
}

func testAccSiteResourceConfig(name string) string {
	return fmt.Sprintf(`
resource "firezone_site" "test" {
  name = %q
}
`, name)
}
