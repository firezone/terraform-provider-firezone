package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccSiteDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "firezone_site" "test" {
  name = "acc-test-ds-site"
}

data "firezone_site" "by_id" {
  id = firezone_site.test.id
}

data "firezone_site" "by_name" {
  name = firezone_site.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.firezone_site.by_id", "name", "firezone_site.test", "name"),
					resource.TestCheckResourceAttrPair("data.firezone_site.by_name", "id", "firezone_site.test", "id"),
				),
			},
		},
	})
}
