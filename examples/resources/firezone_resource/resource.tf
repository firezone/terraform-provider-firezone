resource "firezone_resource" "database" {
  site_id             = firezone_site.main.id
  name                = "postgres-prod"
  type                = "cidr"
  address             = "10.0.1.0/24"
  address_description = "Production Postgres subnet"

  filters {
    protocol = "tcp"
    ports    = ["5432"]
  }
}
