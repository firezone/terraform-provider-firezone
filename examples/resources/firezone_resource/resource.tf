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


# Each Actor can access their own devices through this pool.
resource "firezone_resource" "own_devices" {
  name = "My Devices"
  type = "device_pool"
  device_membership_criteria = {
    mode = "own_devices"
  }
}

resource "firezone_resource" "all_devices" {
  name = "All Devices"
  type = "device_pool"
  device_membership_criteria = {
    mode = "all_devices"
  }
}

data "firezone_group" "engineering" {
  name = "Engineering"
}

resource "firezone_resource" "engineering_devices" {
  name = "Engineering Devices"
  type = "device_pool"
  device_membership_criteria = {
    mode     = "actor_group"
    group_id = data.firezone_group.engineering.id
  }
}

data "firezone_client" "build_machine" {
  firezone_id = "fz-build-machine"
}

# This owns the complete member set. To manage members individually instead,
# omit device_membership_criteria and use firezone_pool_member.
resource "firezone_resource" "build_machines" {
  name = "Build Machines"
  type = "device_pool"
  device_membership_criteria = {
    mode       = "listed"
    device_ids = [data.firezone_client.build_machine.id]
  }
}
