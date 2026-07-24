terraform {
  required_providers {
    firezone = {
      source = "firezone/firezone"
    }
  }
}

provider "firezone" {
  endpoint = "https://api.firezone.dev"
  # token read from the FIREZONE_TOKEN environment variable
}
