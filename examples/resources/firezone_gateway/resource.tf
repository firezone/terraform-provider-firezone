# Provisions a Gateway and mints its single-owner token in one call.
# The token is only ever returned here, at creation - see the
# provider's documentation for the "token" attribute before relying on
# it surviving a rotation or terraform import.
#
# for_each is keyed by a stable identifier ("us-east-1"), not by the
# mutable "name" value itself. With for_each over a set of plain
# strings, Terraform uses each string as the resource's own identity in
# state - so renaming "gw-us-east-1" to anything else would look like
# deleting one map entry and adding another, destroying and recreating
# the Gateway (and minting a brand new token) instead of renaming it in
# place. Keying by a map lets "name" change freely as an ordinary
# attribute update.
resource "firezone_gateway" "gw" {
  for_each = {
    "us-east-1" = "gw-us-east-1"
    "us-east-2" = "gw-us-east-2"
  }
  site_id = firezone_site.main.id
  name    = each.value
}

resource "aws_secretsmanager_secret_version" "gateway_token" {
  for_each      = firezone_gateway.gw
  secret_id     = aws_secretsmanager_secret.gateway_token[each.key].id
  secret_string = each.value.token
}

# Scheduled token rotation. Any change to token_rotation_trigger rotates
# the token and replaces the "token" attribute with the new secret,
# which then flows to whatever consumes it - the secret manager entry
# above, a host's user_data, and so on.
#
# Rotation is not instant, and Terraform cannot finish the job. The old
# token keeps working until the Gateway connects with the replacement or
# the API's grace period elapses, whichever comes first. Whatever
# configures the Gateway host has to pick up the new token and restart
# within that window; if it does not, the Gateway is stranded, and once
# pickup is confirmed the old token is deleted so rolling back will not
# help.
#
# `terraform plan` warns when a rotation is already pending, and refresh
# warns if a rotation happened outside Terraform.
resource "time_rotating" "gateway_token" {
  rotation_days = 90
}

resource "firezone_gateway" "rotating" {
  site_id = firezone_site.main.id
  name    = "gw-us-west-1"

  token_rotation_trigger = time_rotating.gateway_token.id
}
