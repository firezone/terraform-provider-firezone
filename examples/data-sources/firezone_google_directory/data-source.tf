# Looks up a Google Workspace directory connection's id, for use as
# firezone_group's directory_id - useful when a Group name is shared
# across an unsynced Group and one or more synced directories.
data "firezone_google_directory" "corp" {
  name = "Google Directory"
}

data "firezone_group" "engineering_google" {
  name         = "Engineering"
  directory_id = data.firezone_google_directory.corp.id
}
