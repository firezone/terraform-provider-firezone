package provider

import (
	"regexp"
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

data "firezone_actor" "by_email" {
  email = firezone_actor.test.email
}

data "firezone_actor" "by_name_and_email" {
  name  = firezone_actor.test.name
  email = firezone_actor.test.email
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.firezone_actor.by_id", "name", "firezone_actor.test", "name"),
					resource.TestCheckResourceAttrPair("data.firezone_actor.by_name", "id", "firezone_actor.test", "id"),
					// Email is unique per account and survives renames, so
					// it resolves on its own - the point of the lookup.
					resource.TestCheckResourceAttrPair("data.firezone_actor.by_email", "id", "firezone_actor.test", "id"),
					resource.TestCheckResourceAttrPair("data.firezone_actor.by_name_and_email", "id", "firezone_actor.test", "id"),
					resource.TestCheckResourceAttr("data.firezone_actor.by_id", "type", "service_account"),
				),
			},
		},
	})
}

// TestAccActorDataSource_ConfigValidators checks the lookup-key rules:
// something must be set, and id can't be combined with name or email.
// name+email is deliberately absent here - it's a valid combination,
// covered by TestAccActorDataSource above.
func TestAccActorDataSource_ConfigValidators(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// AtLeastOneOf reports a different summary than
				// Conflicting does - "Missing", not "Invalid".
				Config:      `data "firezone_actor" "test" {}`,
				ExpectError: regexp.MustCompile(`Missing Attribute Configuration`),
			},
			{
				Config: `
data "firezone_actor" "test" {
  id   = "42a7f82f-831a-4a9d-8f17-c66c2bb6e205"
  name = "Ada Lovelace"
}
`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
			{
				Config: `
data "firezone_actor" "test" {
  id    = "42a7f82f-831a-4a9d-8f17-c66c2bb6e205"
  email = "ada@example.com"
}
`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
		},
	})
}
