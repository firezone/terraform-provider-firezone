# Clients enroll themselves when a device first connects, so Terraform
# can reference one but never create it - hence a data source with no
# matching resource.

data "firezone_client" "by_id" {
  id = "42a7f82f-831a-4a9d-8f17-c66c2bb6e205"
}

# Prefer firezone_id over name for anything long-lived: names are not
# unique and change when a user renames their device, while the Firezone
# ID is stable. Both are filtered server-side, so either costs one
# request.
data "firezone_client" "by_firezone_id" {
  firezone_id = "fz-abc123"
}

# Looking up by name fails if more than one Client shares it.
data "firezone_client" "by_name" {
  name = "jane-laptop"
}
