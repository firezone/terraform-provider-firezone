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

## [0.4.0] - 2026-10-29

### Changed

- **Breaking:** `firezone_policy` takes `posture` as an attribute holding
  an expression in plain HCL values, in place of the nested `posture`,
  `group` and `check` blocks. A check is one object, `{ field = "...",
  <operator> = <value> }`, and checks combine with `{ all = [...] }`,
  `{ any = [...] }` and `{ not = {...} }`. Values take the attribute's own
  type, so write `true` and `40`, not `"true"` and `"40"`. Rewrite each
  block as follows:

  ```hcl
  # Before
  posture {
    operator = "and"

    check {
      field    = "intune.enrolled"
      operator = "is"
      value    = "true"
    }

    group {
      operator = "or"
      negate   = true

      check {
        field    = "sentinelone.active_threats"
        operator = "gt"
        value    = "0"
      }

      check {
        field    = "intune.compliance_state"
        operator = "is_in"
        values   = ["noncompliant", "error"]
        rows     = "all"
      }
    }
  }

  # After
  posture = {
    all = [
      { field = "intune.enrolled", is = true },
      { not = { any = [
        { field = "sentinelone.active_threats", gt = 0 },
        { field = "intune.compliance_state", is_in = ["noncompliant", "error"], rows = "all" },
      ] } },
    ]
  }
  ```

  A group's `operator = "and"` becomes `all`, `"or"` becomes `any`, and
  `negate = true` wraps the group in `not`. A check's `operator = "x"` and
  `value` or `values` become `x = <value>`. Operators that take no value,
  such as `exists`, are written `exists = true`. A plan with the old blocks
  fails with an error. A plan with a string where the attribute takes a
  boolean or number, such as `is = "true"`, fails with an error that says
  to remove the quotes.

  Existing state is upgraded on the first plan, and the Policy's posture
  is not sent again. Expressions that are the same do not show as a
  change. Changing a posture still revokes the Policy's active
  authorizations, as before.

### Added

- A posture is an ordinary HCL value. You can keep rules in `locals`,
  variables or module outputs, share them between Policies, and generate
  checks with `for` expressions.
- Postures can nest to the API's full limit of 10 levels, and `not` can
  wrap a single check.
- `pkg/provider` Go package with `New(version)`, which returns a
  provider factory for in-process use, e.g. by Upjet-based Crossplane
  providers.

### Security

- Update `golang.org/x/net` to v0.62.0 for GO-2026-6860 and GO-2026-6866.

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

[Unreleased]: https://github.com/firezone/terraform-provider-firezone/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/firezone/terraform-provider-firezone/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/firezone/terraform-provider-firezone/compare/v0.2.0...v0.3.0
