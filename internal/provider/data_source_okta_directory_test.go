package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// There is no firezone_okta_directory resource - directories only
// exist once a real Okta sync has run against the account, which
// acceptance tests can't fabricate. So this only exercises the
// reachable path: looking up a name that doesn't exist.
func TestAccOktaDirectoryDataSource_notFound(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "firezone_okta_directory" "missing" {
  name = "acc-test-does-not-exist"
}
`,
				ExpectError: regexp.MustCompile(`Okta Directory Not Found`),
			},
		},
	})
}
