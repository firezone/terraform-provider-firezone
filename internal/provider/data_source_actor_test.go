package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccActorDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "firezone_actor" "test" {
  name = "acc-test-ds-actor"
  type = "service_account"
}

data "firezone_actor" "by_id" {
  id = firezone_actor.test.id
}

data "firezone_actor" "by_name" {
  name = firezone_actor.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.firezone_actor.by_id", "name", "firezone_actor.test", "name"),
					resource.TestCheckResourceAttrPair("data.firezone_actor.by_name", "id", "firezone_actor.test", "id"),
					resource.TestCheckResourceAttr("data.firezone_actor.by_id", "type", "service_account"),
				),
			},
		},
	})
}
