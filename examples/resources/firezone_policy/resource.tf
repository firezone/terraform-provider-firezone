# Groups synced from an identity provider are looked up via the
# firezone_group data source, not managed as a resource.
data "firezone_group" "engineering" {
  name = "Engineering"
}

resource "firezone_policy" "engineering_app_access" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.internal_app.id
  description = "Engineering access to internal app"

  condition {
    property = "remote_ip_location_region"
    operator = "is_in"
    values   = ["US", "CA"]
  }
}
