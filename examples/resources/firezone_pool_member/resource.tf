# Omit device_membership_criteria to create an empty listed pool and let
# firezone_pool_member manage its members. Parent updates leave them alone.
# Device pools have no site_id or address.
resource "firezone_resource" "field_laptops" {
  name = "field-laptops"
  type = "device_pool"
}

# Pool members are Clients, one firezone_pool_member per Client. Like
# firezone_group_membership, these compose additively: adding or
# removing one leaves the pool's other members alone, so two configs can
# safely manage different members of the same pool.
resource "firezone_pool_member" "jane_laptop" {
  resource_id = firezone_resource.field_laptops.id
  device_id   = data.firezone_client.jane_laptop.id
}

data "firezone_client" "jane_laptop" {
  firezone_id = "fz-abc123"
}

# To add many Clients at once, for_each over their Firezone IDs rather
# than writing a block per device.
variable "field_laptop_firezone_ids" {
  description = "Firezone IDs of the laptops in the field-laptops pool."
  type        = set(string)
  default     = []
}

data "firezone_client" "field_laptops" {
  for_each    = var.field_laptop_firezone_ids
  firezone_id = each.value
}

resource "firezone_pool_member" "field_laptops" {
  for_each    = data.firezone_client.field_laptops
  resource_id = firezone_resource.field_laptops.id
  device_id   = each.value.id
}
