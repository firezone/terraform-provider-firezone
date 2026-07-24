# Looks up a Group synced from an identity provider - these can't be
# created as a firezone_group resource, only looked up.
data "firezone_group" "engineering" {
  name = "Engineering"
}

# If more than one Group shares a name - e.g. a native Group and one
# synced in from an identity provider, both called "Engineering" - set
# directory_id to disambiguate. "" selects the native, unsynced Group;
# any other value selects the Group synced from that directory.
data "firezone_group" "engineering_native" {
  name         = "Engineering"
  directory_id = ""
}

data "firezone_group" "engineering_entra" {
  name         = "Engineering"
  directory_id = "42a7f82f-831a-4a9d-8f17-c66c2bb6e205"
}
