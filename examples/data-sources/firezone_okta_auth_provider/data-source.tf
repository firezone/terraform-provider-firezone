# Auth providers are configured in the Firezone dashboard, which owns
# the OAuth secrets, so there is no matching resource - this is lookup
# only. Its main use is resolving a provider ID for a firezone_policy
# condition on auth_provider_id.
data "firezone_okta_auth_provider" "corp" {
  name = "Corp SSO"
}

data "firezone_okta_auth_provider" "by_id" {
  id = "42a7f82f-831a-4a9d-8f17-c66c2bb6e205"
}

# Restrict a Policy to sessions established through this provider.
resource "firezone_policy" "sso_only" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.internal_app.id
  description = "Engineering: Okta sign-ins only"

  condition {
    property = "auth_provider_id"
    operator = "is_in"
    values   = [data.firezone_okta_auth_provider.corp.id]
  }
}
