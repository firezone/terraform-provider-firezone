# Auth providers are configured in the Firezone dashboard, which owns
# the OAuth secrets, so there is no matching resource - this is lookup
# only. Its main use is resolving a provider ID for a firezone_policy
# condition on auth_provider_id (see the
# firezone_okta_auth_provider example for that).
data "firezone_google_auth_provider" "corp" {
  name = "Google SSO"
}
