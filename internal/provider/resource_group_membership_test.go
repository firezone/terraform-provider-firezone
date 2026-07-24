package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccGroupMembershipResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipResourceConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("firezone_group_membership.test", "group_id", "firezone_group.test", "id"),
					resource.TestCheckResourceAttrPair("firezone_group_membership.test", "actor_id", "firezone_actor.test", "id"),
				),
			},
			{
				ResourceName:      "firezone_group_membership.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccGroupMembershipResource_Additive verifies the load-bearing
// design decision from resource_group_membership.go: two membership
// resources against the same Group must both survive independently
// across an apply, because Create/Delete use the PATCH add/remove
// endpoint rather than the PUT replace-all endpoint.
func TestAccGroupMembershipResource_Additive(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipResourceAdditiveConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("firezone_group_membership.one", "group_id", "firezone_group.test", "id"),
					resource.TestCheckResourceAttrPair("firezone_group_membership.two", "group_id", "firezone_group.test", "id"),
				),
			},
			// Re-apply the same config: if Create ever regresses to PUT
			// replace-all, one of these two memberships would be gone
			// and this step's plan would show a diff.
			{
				Config:   testAccGroupMembershipResourceAdditiveConfig(),
				PlanOnly: true,
			},
		},
	})
}

func testAccGroupMembershipResourceConfig() string {
	return `
resource "firezone_group" "test" {
  name = "acc-test-group"
}

resource "firezone_actor" "test" {
  name = "acc-test-svc"
  type = "service_account"
}

resource "firezone_group_membership" "test" {
  group_id = firezone_group.test.id
  actor_id = firezone_actor.test.id
}
`
}

func testAccGroupMembershipResourceAdditiveConfig() string {
	return `
resource "firezone_group" "test" {
  name = "acc-test-group"
}

resource "firezone_actor" "one" {
  name = "acc-test-svc-one"
  type = "service_account"
}

resource "firezone_actor" "two" {
  name = "acc-test-svc-two"
  type = "service_account"
}

resource "firezone_group_membership" "one" {
  group_id = firezone_group.test.id
  actor_id = firezone_actor.one.id
}

resource "firezone_group_membership" "two" {
  group_id = firezone_group.test.id
  actor_id = firezone_actor.two.id
}
`
}
