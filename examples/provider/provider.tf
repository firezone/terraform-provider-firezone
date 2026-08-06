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

  # The API rate limits per account with a token bucket: roughly 20
  # requests of burst, refilling at about one per second. Terraform's
  # default parallelism of 10 drains that immediately on a sizable apply
  # or destroy, so requests get 429s and the provider retries them.
  #
  # Waits escalate exponentially, never drop below the Retry-After
  # header, and are capped by retry_max_wait_seconds. If a large run
  # still fails, raise the cap before the attempt count - a bigger cap
  # lengthens every later attempt, while more attempts at a small cap
  # just retry sooner and lose the token race again. Lowering
  # `terraform apply -parallelism=N` is the other lever.
  #
  # max_retries            = 10
  # retry_max_wait_seconds = 30
}
