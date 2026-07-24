package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccGroupDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "firezone_group" "test" {
  name = "acc-test-ds-group"
}

data "firezone_group" "by_id" {
  id = firezone_group.test.id
}

data "firezone_group" "by_name" {
  name = firezone_group.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.firezone_group.by_id", "name", "firezone_group.test", "name"),
					resource.TestCheckResourceAttrPair("data.firezone_group.by_name", "id", "firezone_group.test", "id"),
				),
			},
		},
	})
}
