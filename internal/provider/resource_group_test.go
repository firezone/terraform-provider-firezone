package provider

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccGroupResource(t *testing.T) {
	// Manual group names are unique per account, and staging already has
	// manual groups named "Engineering", so use a name unique to this run.
	base := fmt.Sprintf("acc-test-group-%d", time.Now().UnixNano())
	renamed := base + " renamed"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupResourceConfig(base),
				Check:  resource.TestCheckResourceAttr("firezone_group.test", "name", base),
			},
			{
				ResourceName:      "firezone_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccGroupResourceConfig(renamed),
				Check:  resource.TestCheckResourceAttr("firezone_group.test", "name", renamed),
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
