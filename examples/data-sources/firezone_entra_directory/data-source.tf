# Looks up an Entra directory connection's id, for use as
# firezone_group's directory_id - useful when a Group name is shared
# across an unsynced Group and one or more synced directories.
data "firezone_entra_directory" "corp" {
  name = "Entra Directory"
}

data "firezone_group" "engineering_entra" {
  name         = "Engineering"
  directory_id = data.firezone_entra_directory.corp.id
}
