# Multiple firezone_group_membership resources for the same group
# compose additively - each is independently addressable/removable and
# does not disturb membership managed by other resources or configs.
resource "firezone_group_membership" "contractor_access" {
  group_id = firezone_group.contractors.id
  actor_id = firezone_actor.contractor.id
}

# To put a whole group of actors in one Group, for_each over their IDs
# rather than writing one resource block per actor. Key by actor_id
# itself (a stable identifier), not by something that could change -
# see the firezone_gateway example for what goes wrong when a for_each
# key is allowed to mutate.
resource "firezone_actor" "contractor_pool" {
  for_each = toset(["alice", "bob", "carol"])
  name     = each.value
  email    = "${each.value}@example.com"
  type     = "account_user"
}

resource "firezone_group_membership" "contractor_pool_access" {
  for_each = firezone_actor.contractor_pool
  group_id = firezone_group.contractors.id
  actor_id = each.value.id
}
