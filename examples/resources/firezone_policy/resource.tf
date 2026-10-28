# Groups synced from an identity provider are looked up via the
# firezone_group data source, not managed as a resource.
data "firezone_group" "engineering" {
  name = "Engineering"
}

resource "firezone_policy" "engineering_app_access" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.internal_app.id
  description = "Engineering access to internal app"

  condition {
    property = "remote_ip_location_region"
    operator = "is_in"
    values   = ["US", "CA"]
  }
}

# Grant access only to Clients that present a valid certificate from one of
# the account's trust anchors. Fails at apply if the account has no trust
# anchor. With a posture, checks then match only identifiers the certificate
# proves.
resource "firezone_policy" "attested_only" {
  group_id            = data.firezone_group.engineering.id
  resource_id         = firezone_resource.attested_app.id
  require_attestation = true
}

# ---------------------------------------------------------------------------
# Device posture
#
# A posture lists checks a connecting device must pass, in addition to every
# condition, before the Policy grants access. Needs the account's
# device_posture entitlement.
#
# `posture` is an expression built from HCL objects. Each object is one of:
#
#   - { all = [ ... ] }  every item must hold
#   - { any = [ ... ] }  at least one item must hold
#   - { not = { ... } }  the item must not hold
#   - { field = "<provider>.<attribute>", <operator> = <value> }  a check
#
# The provider in a field name is firezone (the connecting device itself),
# intune, iru, defender, santa, sentinelone or sophos. Values take the
# attribute's own type - true, 40, "14.4.1", ["a", "b"] - and the provider
# checks at plan time that the operator and value fit the attribute.
# ---------------------------------------------------------------------------

# 1. The simplest posture: one check. The device must be enrolled in Intune.
resource "firezone_policy" "posture_single_check" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_single_check.id

  posture = { field = "intune.enrolled", is = true }
}

# 2. "all": every check must pass.
resource "firezone_policy" "posture_all_must_pass" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_all_must_pass.id

  posture = {
    all = [
      { field = "intune.enrolled", is = true },
      { field = "intune.compliance_state", is = "compliant" },
    ]
  }
}

# 3. "any": one passing check is enough. Here a device managed by either
# Intune or Iru is accepted.
resource "firezone_policy" "posture_any_may_pass" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_any_may_pass.id

  posture = {
    any = [
      { field = "intune.enrolled", is = true },
      { field = "iru.filevault_enabled", is = true },
    ]
  }
}

# 4. Nesting: (enrolled in Intune) AND (compliant OR in its grace period).
resource "firezone_policy" "posture_nested" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_nested.id

  posture = {
    all = [
      { field = "intune.enrolled", is = true },
      { any = [
        { field = "intune.compliance_state", is = "compliant" },
        { field = "intune.compliance_state", is = "inGracePeriod" },
      ] },
    ]
  }
}

# 5. "none of these": not around an "any". The device must not be infected
# and must not have active threats.
resource "firezone_policy" "posture_none_of" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_none_of.id

  posture = {
    all = [
      { field = "sentinelone.is_active", is = true },
      { not = { any = [
        { field = "sentinelone.infected", is = true },
        { field = "sentinelone.active_threats", gt = 0 },
      ] } },
    ]
  }
}

# 6. "not all of these": not around an "all". Blocks devices that are both
# outdated and decommissioned.
resource "firezone_policy" "posture_not_all_of" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_not_all_of.id

  posture = {
    all = [
      { field = "firezone.last_seen_version", gte = "1.5.0" },
      { not = { all = [
        { field = "sentinelone.is_up_to_date", is = false },
        { field = "sentinelone.is_decommissioned", is = true },
      ] } },
    ]
  }
}

# 7. Strings: is, is_not, contains, does_not_contain, starts_with, ends_with
# and the regular expression operators matches and does_not_match.
# Comparison ignores case.
resource "firezone_policy" "posture_string_operators" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_string_operators.id

  posture = {
    all = [
      { field = "firezone.hostname", starts_with = "corp-" },
      { field = "firezone.name", does_not_contain = "personal" },
      { field = "firezone.device_serial", matches = "^C02[A-Z0-9]{8}$" },
      { field = "iru.platform", is_not = "tvOS" },
    ]
  }
}

# 8. Lists of strings: is_in and is_not_in.
resource "firezone_policy" "posture_string_lists" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_string_lists.id

  posture = {
    all = [
      { field = "intune.compliance_state", is_in = ["compliant", "inGracePeriod"] },
      { field = "defender.health_status", is_not_in = ["Inactive", "ImpairedCommunication", "NoSensorData"] },
    ]
  }
}

# 9. Versions: is, is_not, gt, gte, lt and lte. Write versions as strings;
# 14.4 and 14.4.0 compare equal.
resource "firezone_policy" "posture_versions" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_versions.id

  posture = {
    all = [
      { field = "intune.os_version", gte = "14.4" },
      { field = "santa.santa_version", gte = "2024.6" },
    ]
  }
}

# 10. "@latest": require the newest Firezone Client release for the device's
# platform. Only valid for firezone.last_seen_version.
resource "firezone_policy" "posture_latest_client" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_latest_client.id

  posture = { field = "firezone.last_seen_version", gte = "@latest" }
}

# 11. Numbers: integers and floats take eq, ne, gt, gte, lt and lte.
resource "firezone_policy" "posture_numbers" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_numbers.id

  posture = {
    all = [
      { field = "sentinelone.active_threats", eq = 0 },
      { field = "santa.sip_status", gt = 0 },
      { field = "iru.device_capacity_gb", gte = 0.5 },
    ]
  }
}

# 12. Timestamps: within_last and not_within_last take an ISO 8601 duration
# ("PT24H" is 24 hours, "P30D" 30 days); before and after take an ISO 8601
# datetime.
resource "firezone_policy" "posture_timestamps" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_timestamps.id

  posture = {
    all = [
      # Checked in with Intune in the last 24 hours.
      { field = "intune.last_sync_at", within_last = "PT24H" },
      # Seen by Defender in the last 30 days.
      { field = "defender.last_seen_at", within_last = "P30D" },
      # Enrolled before 2025.
      { field = "intune.enrolled_at", before = "2025-01-01T00:00:00Z" },
    ]
  }
}

# 13. IP addresses: is_in_cidr and is_not_in_cidr take a list of CIDRs, IPv4
# or IPv6.
resource "firezone_policy" "posture_ip_ranges" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_ip_ranges.id

  posture = {
    all = [
      { field = "defender.last_ip_address", is_in_cidr = ["10.0.0.0/8", "fd00::/8"] },
      { field = "firezone.ipv4", is_not_in_cidr = ["10.99.0.0/16"] },
    ]
  }
}

# 14. Lists of values on the device: contains and does_not_contain take one
# value; contains_any_of and contains_all_of take a list; is_empty and
# is_not_empty take none, so they are written `= true`.
resource "firezone_policy" "posture_device_lists" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_device_lists.id

  posture = {
    all = [
      { field = "iru.tags", contains = "managed" },
      { field = "iru.tags", does_not_contain = "quarantined" },
      { field = "defender.machine_tags", contains_any_of = ["corp", "contractor"] },
      { field = "santa.primary_user_groups", contains_all_of = ["engineering", "staff"] },
      { field = "sentinelone.active_protection", is_not_empty = true },
    ]
  }
}

# 15. exists and does_not_exist work on every attribute and take no value.
# A missing attribute fails every other operator - including negative ones
# such as is_not - so test for it first when that matters.
resource "firezone_policy" "posture_exists" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_exists.id

  posture = {
    all = [
      { field = "iru.serial_number", exists = true },
      { field = "santa.temporary_admin_mode_user", does_not_exist = true },
    ]
  }
}

# 16. rows: a device can match several records of an integration provider, for
# example when it is enrolled twice. rows = "any" (the default) holds when any
# matching record passes; rows = "all" only when every one does. It does not
# apply to firezone attributes, which describe exactly one device.
resource "firezone_policy" "posture_rows" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_rows.id

  posture = {
    all = [
      { field = "intune.compliance_state", is = "compliant", rows = "all" },
      { field = "defender.os_up_to_date", is = true },
    ]
  }
}

# 17. Attributes of the connecting device itself, with no integration needed:
# its Client version, certificate attestation, location and hostname.
resource "firezone_policy" "posture_firezone_device" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_firezone_device.id

  posture = {
    all = [
      { field = "firezone.last_seen_version", gte = "1.5.0" },
      { field = "firezone.last_attested_at", within_last = "PT12H" },
      { field = "firezone.last_seen_remote_ip_location_city", is_in = ["San Francisco", "London", "Berlin"] },
    ]
  }
}

# 18. Mixing providers: a Mac must pass Iru's disk encryption and firewall
# checks and be up to date in either Iru or Santa.
resource "firezone_policy" "posture_macos_hardening" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_macos_hardening.id

  posture = {
    all = [
      { field = "iru.filevault_enabled", is = true },
      { field = "iru.firewall_enabled", is = true },
      { any = [
        { field = "iru.os_up_to_date", is = true },
        { field = "santa.os_up_to_date", is = true },
      ] },
    ]
  }
}

# 19. Deeper nesting: A and (B or (C and not D)). The API allows 10 levels of
# all, any and not above a check.
resource "firezone_policy" "posture_deeply_nested" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_deeply_nested.id

  posture = {
    all = [
      { field = "firezone.last_seen_version", gte = "@latest" },
      { any = [
        { field = "intune.enrolled", is = true },
        { all = [
          { field = "sophos.tamper_protection_enabled", is = true },
          { not = { field = "sophos.health_overall", is = "bad" } },
        ] },
      ] },
    ]
  }
}

# 20. Alternatives side by side: (Windows managed) OR (macOS managed) OR
# (Linux with Defender).
resource "firezone_policy" "posture_per_platform" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_per_platform.id

  posture = {
    any = [
      { all = [
        { field = "intune.enrolled", is = true },
        { field = "intune.os_version", gte = "10.0.22621" },
      ] },
      { all = [
        { field = "iru.filevault_enabled", is = true },
        { field = "iru.os_version", gte = "14.4" },
      ] },
      { all = [
        { field = "defender.health_status", is = "Active" },
        { field = "defender.os_platform", is = "Linux" },
      ] },
    ]
  }
}

# 21. Postures together with conditions. Conditions and posture both have to
# pass: here, from the US or Canada during business hours, on a compliant
# device.
resource "firezone_policy" "posture_with_conditions" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_with_conditions.id
  description = "Engineering access from compliant devices during business hours"

  condition {
    property = "remote_ip_location_region"
    operator = "is_in"
    values   = ["US", "CA"]
  }

  condition {
    property = "current_utc_datetime"
    operator = "is_in_day_of_week_time_ranges"
    values = [
      "M/09:00-17:00/America/New_York",
      "T/09:00-17:00/America/New_York",
      "W/09:00-17:00/America/New_York",
      "R/09:00-17:00/America/New_York",
      "F/09:00-17:00/America/New_York",
    ]
  }

  posture = { field = "intune.compliance_state", is = "compliant" }
}

# 22. Building a posture from variables. Any value can come from an
# expression; here the minimum OS version and allowed states are inputs.
variable "minimum_os_version" {
  type    = string
  default = "14.4"
}

variable "allowed_compliance_states" {
  type    = list(string)
  default = ["compliant", "inGracePeriod"]
}

resource "firezone_policy" "posture_from_variables" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_from_variables.id

  posture = {
    all = [
      { field = "intune.os_version", gte = var.minimum_os_version },
      { field = "intune.compliance_state", is_in = var.allowed_compliance_states },
    ]
  }
}

# 23. Generating checks with a for expression. Each Intune compliance state
# to refuse becomes one check inside a "not any".
locals {
  refused_states = ["noncompliant", "error", "conflict", "configManager"]
}

resource "firezone_policy" "posture_generated_checks" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_generated_checks.id

  posture = {
    all = [
      { field = "intune.enrolled", is = true },
      { not = { any = [
        for state in local.refused_states : { field = "intune.compliance_state", is = state }
      ] } },
    ]
  }
}

# 24. Reusing rules. A posture is an ordinary value, so a rule written once
# in locals (or a module output) can be shared by every Policy that needs it.
locals {
  managed_mac = {
    all = [
      { field = "iru.filevault_enabled", is = true },
      { field = "iru.firewall_enabled", is = true },
    ]
  }
  current_client = { field = "firezone.last_seen_version", gte = "@latest" }
}

resource "firezone_policy" "posture_shared_rules" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_shared_rules.id

  posture = { all = [local.managed_mac, local.current_client] }
}

# 25. A posture on a disabled Policy: written now, switched on later by
# setting enabled = true. Changing a posture revokes the Policy's active
# authorizations, so connections reauthorize.
resource "firezone_policy" "posture_staged" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_staged.id
  enabled     = false

  posture = { field = "intune.compliance_state", is = "compliant" }
}
