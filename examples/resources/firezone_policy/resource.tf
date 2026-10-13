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

# ---------------------------------------------------------------------------
# Device posture
#
# A posture lists checks a connecting device must pass, in addition to every
# condition, before the Policy grants access. Needs the account's
# device_posture entitlement.
#
# The `posture` block is the root group of an and/or expression:
#
#   - `operator` is "and" (every check and group must hold) or "or" (at least
#     one must).
#   - `check` blocks compare one device attribute, named "<provider>.<attribute>",
#     against a value. The provider is firezone (the connecting device itself),
#     intune, iru, defender, santa, sentinelone or sophos.
#   - `group` blocks nest another and/or group, up to the API's limit of 10
#     levels and 100 checks.
#   - `negate = true` inverts a group.
#
# `value` is always a string, whatever the attribute's type: "true", "40",
# "14.4.1". The list operators take `values` instead. Which operators an
# attribute accepts depends on its type, and the provider checks this at plan
# time.
# ---------------------------------------------------------------------------

# 1. The simplest posture: one check. The device must be enrolled in Intune.
resource "firezone_policy" "posture_single_check" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_single_check.id

  posture {
    operator = "and"

    check {
      field    = "intune.enrolled"
      operator = "is"
      value    = "true"
    }
  }
}

# 2. "and": every check must pass.
resource "firezone_policy" "posture_all_must_pass" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_all_must_pass.id

  posture {
    operator = "and"

    check {
      field    = "intune.enrolled"
      operator = "is"
      value    = "true"
    }

    check {
      field    = "intune.compliance_state"
      operator = "is"
      value    = "compliant"
    }
  }
}

# 3. "or": any one check is enough. Here a device managed by either Intune or
# Iru is accepted.
resource "firezone_policy" "posture_any_may_pass" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_any_may_pass.id

  posture {
    operator = "or"

    check {
      field    = "intune.enrolled"
      operator = "is"
      value    = "true"
    }

    check {
      field    = "iru.filevault_enabled"
      operator = "is"
      value    = "true"
    }
  }
}

# 4. Nested groups: (enrolled in Intune) AND (compliant OR in its grace
# period). A group is combined with its siblings by the enclosing operator.
resource "firezone_policy" "posture_nested" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_nested.id

  posture {
    operator = "and"

    check {
      field    = "intune.enrolled"
      operator = "is"
      value    = "true"
    }

    group {
      operator = "or"

      check {
        field    = "intune.compliance_state"
        operator = "is"
        value    = "compliant"
      }

      check {
        field    = "intune.compliance_state"
        operator = "is"
        value    = "inGracePeriod"
      }
    }
  }
}

# 5. Negation: "none of these". A negated "or" group holds only when no check
# inside it holds - here, the device must not be infected and must not have
# active threats.
resource "firezone_policy" "posture_none_of" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_none_of.id

  posture {
    operator = "and"

    check {
      field    = "sentinelone.is_active"
      operator = "is"
      value    = "true"
    }

    group {
      operator = "or"
      negate   = true

      check {
        field    = "sentinelone.infected"
        operator = "is"
        value    = "true"
      }

      check {
        field    = "sentinelone.active_threats"
        operator = "gt"
        value    = "0"
      }
    }
  }
}

# 6. Negation: "not all of these". A negated "and" group holds unless every
# check inside it holds.
resource "firezone_policy" "posture_not_all_of" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_not_all_of.id

  posture {
    operator = "and"

    check {
      field    = "firezone.last_seen_version"
      operator = "gte"
      value    = "1.5.0"
    }

    # Block devices that are both outdated and decommissioned.
    group {
      operator = "and"
      negate   = true

      check {
        field    = "sentinelone.is_up_to_date"
        operator = "is"
        value    = "false"
      }

      check {
        field    = "sentinelone.is_decommissioned"
        operator = "is"
        value    = "true"
      }
    }
  }
}

# 7. Strings: is, is_not, contains, does_not_contain, starts_with, ends_with
# and the regular expression operators matches and does_not_match. Comparison
# ignores case.
resource "firezone_policy" "posture_string_operators" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_string_operators.id

  posture {
    operator = "and"

    check {
      field    = "firezone.hostname"
      operator = "starts_with"
      value    = "corp-"
    }

    check {
      field    = "firezone.name"
      operator = "does_not_contain"
      value    = "personal"
    }

    check {
      field    = "firezone.device_serial"
      operator = "matches"
      value    = "^C02[A-Z0-9]{8}$"
    }

    check {
      field    = "iru.platform"
      operator = "is_not"
      value    = "tvOS"
    }
  }
}

# 8. A list of strings: is_in and is_not_in take `values`.
resource "firezone_policy" "posture_string_lists" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_string_lists.id

  posture {
    operator = "and"

    check {
      field    = "intune.compliance_state"
      operator = "is_in"
      values   = ["compliant", "inGracePeriod"]
    }

    check {
      field    = "defender.health_status"
      operator = "is_not_in"
      values   = ["Inactive", "ImpairedCommunication", "NoSensorData"]
    }
  }
}

# 9. Versions: compare dotted versions with is, is_not, gt, gte, lt and lte.
# 14.4 and 14.4.0 compare equal.
resource "firezone_policy" "posture_versions" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_versions.id

  posture {
    operator = "and"

    check {
      field    = "intune.os_version"
      operator = "gte"
      value    = "14.4"
    }

    check {
      field    = "santa.santa_version"
      operator = "gte"
      value    = "2024.6"
    }
  }
}

# 10. "@latest": require the newest Firezone Client release for the device's
# platform. Only valid for firezone.last_seen_version.
resource "firezone_policy" "posture_latest_client" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_latest_client.id

  posture {
    operator = "and"

    check {
      field    = "firezone.last_seen_version"
      operator = "gte"
      value    = "@latest"
    }
  }
}

# 11. Numbers: integers take eq, ne, gt, gte, lt and lte. Write the number as
# a string in its shortest form ("40", "0.5" - not "040" or "0.50").
resource "firezone_policy" "posture_numbers" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_numbers.id

  posture {
    operator = "and"

    check {
      field    = "sentinelone.active_threats"
      operator = "eq"
      value    = "0"
    }

    check {
      field    = "santa.sip_status"
      operator = "gt"
      value    = "0"
    }

    check {
      field    = "iru.device_capacity_gb"
      operator = "gte"
      value    = "0.5"
    }
  }
}

# 12. Timestamps: within_last and not_within_last take an ISO 8601 duration
# ("PT24H" is 24 hours, "P30D" 30 days); before and after take an ISO 8601
# datetime.
resource "firezone_policy" "posture_timestamps" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_timestamps.id

  posture {
    operator = "and"

    # Checked in with Intune in the last 24 hours.
    check {
      field    = "intune.last_sync_at"
      operator = "within_last"
      value    = "PT24H"
    }

    # Seen by Defender in the last 30 days.
    check {
      field    = "defender.last_seen_at"
      operator = "within_last"
      value    = "P30D"
    }

    # Enrolled before 2025.
    check {
      field    = "intune.enrolled_at"
      operator = "before"
      value    = "2025-01-01T00:00:00Z"
    }
  }
}

# 13. IP addresses: is_in_cidr and is_not_in_cidr take a list of CIDRs, IPv4
# or IPv6.
resource "firezone_policy" "posture_ip_ranges" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_ip_ranges.id

  posture {
    operator = "and"

    check {
      field    = "defender.last_ip_address"
      operator = "is_in_cidr"
      values   = ["10.0.0.0/8", "fd00::/8"]
    }

    check {
      field    = "firezone.ipv4"
      operator = "is_not_in_cidr"
      values   = ["10.99.0.0/16"]
    }
  }
}

# 14. Lists of values on the device: contains and does_not_contain take one
# value; contains_any_of and contains_all_of take `values`; is_empty and
# is_not_empty take neither.
resource "firezone_policy" "posture_device_lists" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_device_lists.id

  posture {
    operator = "and"

    check {
      field    = "iru.tags"
      operator = "contains"
      value    = "managed"
    }

    check {
      field    = "iru.tags"
      operator = "does_not_contain"
      value    = "quarantined"
    }

    check {
      field    = "defender.machine_tags"
      operator = "contains_any_of"
      values   = ["corp", "contractor"]
    }

    check {
      field    = "santa.primary_user_groups"
      operator = "contains_all_of"
      values   = ["engineering", "staff"]
    }

    check {
      field    = "sentinelone.active_protection"
      operator = "is_not_empty"
    }
  }
}

# 15. exists and does_not_exist work on every attribute and take no value.
# A missing attribute fails every other operator - including negative ones
# such as is_not - so test for it first when that matters.
resource "firezone_policy" "posture_exists" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_exists.id

  posture {
    operator = "and"

    check {
      field    = "iru.serial_number"
      operator = "exists"
    }

    check {
      field    = "santa.temporary_admin_mode_user"
      operator = "does_not_exist"
    }
  }
}

# 16. rows: a device can match several records of an integration provider, for
# example when it is enrolled twice. rows = "any" (the default) holds when any
# matching record passes; rows = "all" only when every one does. It does not
# apply to firezone attributes, which describe exactly one device.
resource "firezone_policy" "posture_rows" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_rows.id

  posture {
    operator = "and"

    check {
      field    = "intune.compliance_state"
      operator = "is"
      value    = "compliant"
      rows     = "all"
    }

    check {
      field    = "defender.os_up_to_date"
      operator = "is"
      value    = "true"
      rows     = "any"
    }
  }
}

# 17. Attributes of the connecting device itself, with no integration needed:
# its Client version, certificate attestation, location and hostname.
resource "firezone_policy" "posture_firezone_device" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_firezone_device.id

  posture {
    operator = "and"

    check {
      field    = "firezone.last_seen_version"
      operator = "gte"
      value    = "1.5.0"
    }

    check {
      field    = "firezone.last_attested_at"
      operator = "within_last"
      value    = "PT12H"
    }

    check {
      field    = "firezone.last_seen_remote_ip_location_city"
      operator = "is_in"
      values   = ["San Francisco", "London", "Berlin"]
    }
  }
}

# 18. Mixing providers: a Mac must pass Iru's disk encryption and firewall
# checks and be up to date in either Iru or Santa.
resource "firezone_policy" "posture_macos_hardening" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_macos_hardening.id

  posture {
    operator = "and"

    check {
      field    = "iru.filevault_enabled"
      operator = "is"
      value    = "true"
    }

    check {
      field    = "iru.firewall_enabled"
      operator = "is"
      value    = "true"
    }

    group {
      operator = "or"

      check {
        field    = "iru.os_up_to_date"
        operator = "is"
        value    = "true"
      }

      check {
        field    = "santa.os_up_to_date"
        operator = "is"
        value    = "true"
      }
    }
  }
}

# 19. Deeper nesting: (A and (B or (C and not D))). Groups nest to 10 levels
# in the API; a negated group counts for two.
resource "firezone_policy" "posture_deeply_nested" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_deeply_nested.id

  posture {
    operator = "and"

    check {
      field    = "firezone.last_seen_version"
      operator = "gte"
      value    = "@latest"
    }

    group {
      operator = "or"

      check {
        field    = "intune.enrolled"
        operator = "is"
        value    = "true"
      }

      group {
        operator = "and"

        check {
          field    = "sophos.tamper_protection_enabled"
          operator = "is"
          value    = "true"
        }

        group {
          operator = "or"
          negate   = true

          check {
            field    = "sophos.health_overall"
            operator = "is"
            value    = "bad"
          }
        }
      }
    }
  }
}

# 20. Several groups side by side: (Windows managed) OR (macOS managed) OR
# (Linux with Defender). Each `group` is one alternative.
resource "firezone_policy" "posture_per_platform" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_per_platform.id

  posture {
    operator = "or"

    group {
      operator = "and"

      check {
        field    = "intune.enrolled"
        operator = "is"
        value    = "true"
      }

      check {
        field    = "intune.os_version"
        operator = "gte"
        value    = "10.0.22621"
      }
    }

    group {
      operator = "and"

      check {
        field    = "iru.filevault_enabled"
        operator = "is"
        value    = "true"
      }

      check {
        field    = "iru.os_version"
        operator = "gte"
        value    = "14.4"
      }
    }

    group {
      operator = "and"

      check {
        field    = "defender.health_status"
        operator = "is"
        value    = "Active"
      }

      check {
        field    = "defender.os_platform"
        operator = "is"
        value    = "Linux"
      }
    }
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

  posture {
    operator = "and"

    check {
      field    = "intune.compliance_state"
      operator = "is"
      value    = "compliant"
    }
  }
}

# 22. Building a posture from variables. Any argument can come from an
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

  posture {
    operator = "and"

    check {
      field    = "intune.os_version"
      operator = "gte"
      value    = var.minimum_os_version
    }

    check {
      field    = "intune.compliance_state"
      operator = "is_in"
      values   = var.allowed_compliance_states
    }
  }
}

# 23. Repeating a check from a list with a dynamic block. Each Intune
# compliance state to refuse becomes one check inside a negated "or" group.
locals {
  refused_states = ["noncompliant", "error", "conflict", "configManager"]
}

resource "firezone_policy" "posture_dynamic_checks" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_dynamic_checks.id

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

      dynamic "check" {
        for_each = local.refused_states

        content {
          field    = "intune.compliance_state"
          operator = "is"
          value    = check.value
        }
      }
    }
  }
}

# 24. A posture on a disabled Policy: written now, switched on later by
# setting enabled = true. Changing a posture revokes the Policy's active
# authorizations, so connections reauthorize.
resource "firezone_policy" "posture_staged" {
  group_id    = data.firezone_group.engineering.id
  resource_id = firezone_resource.posture_staged.id
  enabled     = false

  posture {
    operator = "and"

    check {
      field    = "intune.compliance_state"
      operator = "is"
      value    = "compliant"
    }
  }
}
