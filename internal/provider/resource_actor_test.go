package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccActorResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccActorResourceConfig("acc-test-svc", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("firezone_actor.test", "name", "acc-test-svc"),
					resource.TestCheckResourceAttr("firezone_actor.test", "type", "service_account"),
					resource.TestCheckResourceAttr("firezone_actor.test", "enabled", "true"),
				),
			},
			{
				ResourceName:      "firezone_actor.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccActorResourceConfig("acc-test-svc", false),
				Check:  resource.TestCheckResourceAttr("firezone_actor.test", "enabled", "false"),
			},
		},
	})
}

func testAccActorResourceConfig(name string, enabled bool) string {
	return fmt.Sprintf(`
resource "firezone_actor" "test" {
  name    = %q
  type    = "service_account"
  enabled = %t
}
`, name, enabled)
}
