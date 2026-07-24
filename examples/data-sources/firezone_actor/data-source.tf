data "firezone_actor" "existing_admin" {
  name = "Jane Doe"
}

# Actor names aren't unique - if two Actors are both named "Jane Doe",
# set email too to disambiguate.
data "firezone_actor" "existing_admin_by_email" {
  name  = "Jane Doe"
  email = "jane.doe@example.com"
}
