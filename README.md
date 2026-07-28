# terraform-provider-firezone

A Terraform provider for [Firezone](https://www.firezone.dev), built on
`terraform-plugin-framework` and the
[`firezone-go`](https://github.com/firezone/firezone-go) SDK

Manages Sites, Gateways, Resources, Policies, Groups, Group Memberships,
and Actors as resources. Looks up Sites, Groups, Actors, and IdP-synced
directory connections (Entra, Google Workspace, Okta) as data sources.

Generated resource/data-source reference lives in [`docs/`](docs/).
Worked examples for every resource and data source live in
[`examples/`](examples/) and are rendered into `docs/index.md`.

## Provider configuration

```hcl
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
```

`endpoint` (or `FIREZONE_ENDPOINT`) is always the **bare API host**.

`token` (or `FIREZONE_TOKEN`) is the bearer token for an `api_client`
actor, marked sensitive.

## Gateway tokens and secret managers

`firezone_gateway`'s `token` attribute is an ordinary
`Sensitive: true, Computed: true` string, and no special support is
needed to hand it to a cloud secret manager:

```hcl
resource "firezone_gateway" "gw" {
  site_id = firezone_site.main.id
  name    = "gw-nyc-1"
}

resource "aws_secretsmanager_secret" "gateway_token" {
  name = "firezone/gateway-tokens/gw-nyc-1"
}

resource "aws_secretsmanager_secret_version" "gateway_token" {
  secret_id     = aws_secretsmanager_secret.gateway_token.id
  secret_string = firezone_gateway.gw.token
}
```

Two things worth knowing before relying on this:

- **State persistence.** With `secret_string` (the normal argument),
  the token ends up in Terraform state twice. Once in
  `firezone_gateway` and once in `aws_secretsmanager_secret_version`.
  That's inherent to Terraform's standard resource model. Encrypt state
  at rest (e.g. an S3 + KMS backend) rather than trying to avoid this.
- **Write-only arguments.** Terraform 1.11+ and recent provider
  versions support write-only arguments (e.g. `secret_string_wo`) that
  pass a value through without ever persisting it to state. `token` is
  a plain string and is already compatible with feeding one of these;
  swap `secret_string` for `secret_string_wo` on the consumer side if
  you want zero state footprint for the token.

**The API mints a Gateway's token once, on creation, and never
re-exposes it.** Concretely:

- Out-of-band token rotation leaves Terraform state silently holding a
  stale token, with no way for `terraform plan` to detect the drift.
- `terraform import firezone_gateway.gw <site_id>/<gateway_id>` can
  adopt a Gateway into state for rename/delete lifecycle management,
  but can never recover a working token. The imported resource's
  `token` is left empty with a warning.

This is a property of the underlying API, not a provider limitation to
work around.

## Defining a group of Actors

`firezone_group_membership` grants one Actor membership in one Group.
It's a separate resource, not a `members` list attribute folded onto
`firezone_group`. A list attribute would have to own the *entire*
membership set on every apply, so two independent `.tf` configs (e.g.
one owning a Group, another just granting a service account access to
it) would fight over it, with the last apply silently winning. Separate
resources compose additively instead, so adding or removing one doesn't
touch any other.

For a single Actor, write the resource directly:

```hcl
resource "firezone_group_membership" "contractor_access" {
  group_id = firezone_group.contractors.id
  actor_id = firezone_actor.contractor.id
}
```

For many Actors in one Group, `for_each` over their IDs rather than
writing one resource block per Actor:

```hcl
resource "firezone_actor" "contractor_pool" {
  for_each = toset(["alice", "bob", "carol"])
  name     = each.value
  email    = "${each.value}@example.com"
  type     = "account_user"
}

resource "firezone_group_membership" "contractor_pool_access" {
  for_each = firezone_actor.contractor_pool
  group_id = firezone_group.contractors.id
  actor_id = each.value.id
}
```

Key the `for_each` by something stable, here `firezone_actor`'s own
resource map, keyed by the literal strings passed to its own `for_each`,
never by a value that could change later. See
[`examples/resources/firezone_gateway/resource.tf`](examples/resources/firezone_gateway/resource.tf)
for what goes wrong when a `for_each` key is allowed to mutate.
Terraform destroys and recreates the resource instead of updating it in
place, since changing the key removes one map entry and adds another.

Service accounts are just `firezone_actor` with `type = "service_account"`
(no `email`, since the API rejects one for that type):

```hcl
resource "firezone_actor" "ci_deployer" {
  name = "ci-deployer"
  type = "service_account"
}
```

## Disambiguating name lookups

Group and Actor names aren't guaranteed unique. `firezone_group` can
return two matches for the same name if one Group is native to Firezone
and another is synced from an identity provider (or two directories
both sync a Group with that name); `firezone_actor` can do the same the
moment two people share a name. A lookup by name that matches more than
one record fails loudly rather than silently picking one:

```hcl
data "firezone_group" "engineering" {
  name = "Engineering"
}
```

If that fails with "Ambiguous Group Name", set `directory_id` to
disambiguate (`""` selects the native, unsynced Group). Use
`firezone_entra_directory`/`firezone_google_directory`/`firezone_okta_directory`
to look up a directory's id by name, rather than hardcoding it:

```hcl
data "firezone_entra_directory" "corp" {
  name = "Entra Directory"
}

data "firezone_group" "engineering_entra" {
  name         = "Engineering"
  directory_id = data.firezone_entra_directory.corp.id
}
```

`firezone_actor` disambiguates the same way, via `email` instead of
`directory_id`:

```hcl
data "firezone_actor" "jane" {
  name  = "Jane Doe"
  email = "jane.doe@example.com"
}
```

The three directory data sources are read-only lookups for a directory
connection's id and its one distinguishing field (`tenant_id`, `domain`,
or `okta_domain`) - directory connections themselves can only be
created via a real identity-provider sync configured in the dashboard,
not through Terraform.

## Local development

Point the provider at a local dev server without publishing anything,
using Terraform's `dev_overrides`:

```bash
# in the firezone/firezone monorepo checkout, in one terminal:
cd elixir && mix phx.server

# in another, from this repo:
go build -o terraform-provider-firezone .

token=$(cd /path/to/firezone/elixir && MIX_ENV=dev mix run --no-start script/seed_api_client_token.exs | tail -1)
export FIREZONE_ENDPOINT=http://localhost:13001
export FIREZONE_TOKEN=$token
```

`~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "registry.terraform.io/firezone/firezone" = "/absolute/path/to/terraform-provider-firezone"
  }
  direct {}
}
```

Then `terraform plan`/`apply`/`destroy` against any `.tf` file (e.g.
one of `examples/resources/*/resource.tf`); no `terraform init` needed
while `dev_overrides` is active.

## Testing

```bash
mise run test              # unit tests, no server needed
mise run test-acceptance   # TF_ACC=1 acceptance tests, requires FIREZONE_ENDPOINT/FIREZONE_TOKEN
mise run tfdocs             # regenerate docs/ after a schema change
```

Acceptance tests need a running `firezone/firezone` dev server (Postgres
+ `mix phx.server`) and a token minted via that repo's
`elixir/script/seed_api_client_token.exs`; see "Local development"
above.
