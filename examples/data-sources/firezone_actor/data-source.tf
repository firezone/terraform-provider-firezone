data "firezone_actor" "existing_admin" {
  name = "Jane Doe"
}

# Email is unique per account and survives renames, so it works as a
# lookup key on its own - usually a better choice than name.
data "firezone_actor" "by_email" {
  email = "jane.doe@example.com"
}

# Actor names aren't unique - if two Actors are both named "Jane Doe",
# set email too to disambiguate.
data "firezone_actor" "existing_admin_by_email" {
  name  = "Jane Doe"
  email = "jane.doe@example.com"
}
