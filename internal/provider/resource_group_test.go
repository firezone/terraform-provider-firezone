package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccGroupResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupResourceConfig("Engineering"),
				Check:  resource.TestCheckResourceAttr("firezone_group.test", "name", "Engineering"),
			},
			{
				ResourceName:      "firezone_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccGroupResourceConfig("Engineering Renamed"),
				Check:  resource.TestCheckResourceAttr("firezone_group.test", "name", "Engineering Renamed"),
			},
		},
	})
}

func testAccGroupResourceConfig(name string) string {
	return fmt.Sprintf(`
resource "firezone_group" "test" {
  name = %q
}
`, name)
}
