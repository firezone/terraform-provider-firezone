# Every Client owned by one Actor, for example to put all of a user's
# devices in a device pool.
data "firezone_actor" "jane" {
  email = "jane.doe@example.com"
}

data "firezone_clients" "jane" {
  actor_id = data.firezone_actor.jane.id
}

resource "firezone_pool_member" "jane_devices" {
  for_each = { for c in data.firezone_clients.jane.clients : c.id => c }

  resource_id = firezone_resource.field_laptops.id
  device_id   = each.key
}

# Headless Clients that have been disconnected since before 2026. Setting
# online = false leaves out Clients that are still on a long-running
# connection that began before that time.
data "firezone_clients" "stale_headless" {
  client_type      = "headless"
  online           = false
  last_seen_before = "2026-01-01T00:00:00Z"
}
