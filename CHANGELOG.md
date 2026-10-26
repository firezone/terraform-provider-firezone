# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Because this is a Terraform provider, "breaking" means a change that makes
an existing configuration fail to plan, or that removes an attribute that
a configuration can read. A new optional argument or a new computed
attribute is additive, thus not breaking.

While this project is at 0.x, breaking changes bump the minor version and
are listed under `### Changed`, with a note on what to do about them. From
1.0.0 onward they will require a new major version.

## [Unreleased]

## [0.3.0] - 2026-10-26

### Changed

- **Breaking:** `firezone_policy` no longer accepts a `condition` block
  with `property = "device_attested"`. The API removed that condition.
  Use the new `require_attestation` argument instead:

  ```hcl
  # Before
  resource "firezone_policy" "example" {
    # ...
    condition {
      property = "device_attested"
      operator = "is"
      values   = ["true"]
    }
  }

  # After
  resource "firezone_policy" "example" {
    # ...
    require_attestation = true
  }
  ```

  If the condition's value was `"false"`, remove the block and do not set
  `require_attestation`. A plan with the old block fails with an error
  that names this fix.
- **Breaking:** `firezone_entra_auth_provider` no longer has an `issuer`
  attribute. Entra providers no longer have an issuer in the API. Read
  the new `tenant_ids` attribute to get the tenants that can sign in.
- A `firezone_policy` with one or more `condition` blocks needs the
  account's `policy_conditions` entitlement. Without it, the apply fails
  with a 403 error. You can always remove conditions, also after a
  downgrade.

### Added

- `require_attestation` on `firezone_policy`. When true, the Policy
  grants access only to a Client that presented a valid certificate from
  one of the account's trust anchors. The default is false. Setting true
  fails if the account has no trust anchor.
- `tenant_ids` on the `firezone_entra_auth_provider` data source.
- `workspace_domains` on the `firezone_google_auth_provider` data source.
- The `firezone_clients` data source. It lists Clients and can filter
  them by `actor_id`, `name`, `firezone_id`, `online`, `last_seen_before`,
  `last_seen_after`, and `client_type`.
- The `firezone_group_members` data source. It lists the members of a
  Group with their `id`, `name`, `email`, and `type`. It also works for
  synced Groups and the Everyone Group.
- `type` on the `firezone_group` data source: `static`, or `managed` for
  the Everyone Group.
- `disabled_by_directory_id` on the `firezone_actor` data source.
- The `firezone_github_auth_provider` data source.

### Fixed

- `internal/genposture` writes the generated posture file with 0600
  permissions.

[Unreleased]: https://github.com/firezone/terraform-provider-firezone/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/firezone/terraform-provider-firezone/compare/v0.2.0...v0.3.0
