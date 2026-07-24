# Only for Groups you want Terraform to own. Groups synced from an
# identity provider are read-only via the API - look those up with the
# firezone_group data source instead.
resource "firezone_group" "contractors" {
  name = "Contractors"
}
