data "firezone_group" "engineering" {
  name = "Engineering"
}

data "firezone_group_members" "engineering" {
  group_id = data.firezone_group.engineering.id
}

output "engineering_emails" {
  value = [for m in data.firezone_group_members.engineering.members : m.email if m.email != null]
}

# The built-in Everyone Group has type "managed". Its members are every
# user and admin Actor in the account.
data "firezone_group" "everyone" {
  name = "Everyone"
}

data "firezone_group_members" "everyone" {
  group_id = data.firezone_group.everyone.id
}
