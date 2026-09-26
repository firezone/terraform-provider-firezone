# REST API coverage audit

Compared on 2026-09-26 against `firezone/main/elixir` at
[`48052865fdbd492eeaf471a8aee3942c4f7bcf17`](https://github.com/firezone/firezone/tree/48052865fdbd492eeaf471a8aee3942c4f7bcf17/elixir),
using `lib/portal_api/router.ex`, controller write fields, schema definitions,
and `priv/static/openapi.json`. Provider baseline: `b9e9b3e`, plus this PR.
This is source-level coverage, not a claim about rollout to every hosted account.

## Write capabilities

| REST API capability | Provider coverage | Gap / next step |
| --- | --- | --- |
| Sites: create, read, update, delete | `firezone_site` resource and lookup | Writable fields covered. |
| Resources: create, read, update, delete | `firezone_resource` | This PR adds current `device_pool` and all four membership criteria modes. CIDR, IP and DNS fields were already covered. |
| Listed pool membership: list, replace, patch | `firezone_pool_member`; inline listed criteria in this PR | Individual resources patch additively; inline criteria replace the full set. Dynamic criteria have no pool-members endpoint. |
| Policies: create, read, update, delete | `firezone_policy` | **Missing `postures`**, the nested device posture expression. Existing conditions, enabled/disabled state (`enabled` maps to `is_disabled`), and flow-log uploads are covered. Postures need expression validation, null-to-clear semantics, read-back, import and drift tests. Non-null postures require Enterprise entitlement. |
| Actors: create, read, update, delete | `firezone_actor` resource and lookup | Writable fields covered, including email OTP and enabled/disabled state. |
| Groups: create, read, update, delete | `firezone_group` resource and lookup | Writable name covered; directory-managed restrictions remain API-enforced. |
| Group memberships: list, replace, patch | `firezone_group_membership` | Additive single-member ownership is supported. No authoritative full-set resource or membership-list data source. |
| Gateways: provision, read, rename, delete | `firezone_gateway` | Lifecycle covered. Operational response fields are not exposed (see below). |
| Gateway token: create for gateway, rotate | Provisioning returns token; `token_rotation_trigger` rotates | No standalone token resource or way to attach/recover a token for an imported gateway except rotation. The API cannot return an existing secret. |
| Site gateway tokens: create, revoke one, revoke all | None | **Missing token lifecycle** for site enrollment tokens. A dedicated resource needs write-once sensitive state and an explicit refresh strategy because there is no list/get endpoint. Revoke-all is an operational action, not ordinary resource ownership. |
| Actor client tokens: list, get, create, revoke one/all | None | **Missing client-token resource/data source**, including expiration and one-time provisioning secret. Useful for service-account deployment. Bulk revocation is a separate operational concern. |
| Clients: rename/change slug, delete, verify/unverify | `firezone_client` lookup only | **Missing management of enrolled clients and verification status**. No REST create endpoint: use an adoption-oriented design rather than pretending Terraform enrolls devices. |
| Actor external identities: list, get, delete | None | Missing lookup and revocation. Creation/update are directory-owned and have no REST endpoints. |

The API also accepts Internet Resource creation only for the managed Internet
Site, while updates/deletion of Internet Resources return 403. The provider does
not offer `type = "internet"`; this asymmetric lifecycle needs a separate design,
not simply another allowed enum value. This PR leaves it unchanged.

## Read-only endpoints and lookups

| REST API surface | Provider coverage | Gap |
| --- | --- | --- |
| `/account` | None | Account identity, slug, legal name and limits lookup. |
| `/resources`, `/policies`, `/sites/:site_id/gateways` | Managed-resource refresh only | No standalone Resource, Policy or Gateway data sources; consumers must already manage/import them or supply IDs. |
| `/actors`, `/groups`, `/sites`, `/clients` | Singular lookup data sources | No plural inventory/list data sources or full API filtering surface. |
| `/email_otp_auth_providers`, `/oidc_auth_providers`, `/google_auth_providers`, `/entra_auth_providers`, `/okta_auth_providers` | Singular data sources | Core identity/configuration lookup supported; response-field gaps below. |
| `/x509_auth_provider` | None | Missing singleton X.509 auth-provider lookup. `device_attested` policy conditions already exist in the provider. |
| `/google_directories`, `/entra_directories`, `/okta_directories` | Singular data sources | Basic connection lookup supported; status and sync settings omitted. |
| `/intune_posture_providers`, `/iru_posture_providers`, `/defender_posture_providers`, `/santa_posture_providers`, `/sentinelone_posture_providers` | None | Missing all five posture-provider data sources. These endpoints are read-only. |
| `/intune_devices`, `/iru_devices`, `/defender_devices`, `/santa_devices`, `/sentinelone_devices` | None | Missing synced posture-device inventory/lookups. SentinelOne's show route identifies an agent rather than a UUID. |
| `/logs`, `/logs/:log_id` | None | Operational log lookup; lower priority for declarative infrastructure. |

There are no REST write endpoints for auth providers, directories, posture
providers, their synced device inventories, or account settings. Their portal
configuration cannot be added to Terraform solely through the current public
REST surface. X.509 trust-anchor management likewise has no route here.

## Fields omitted from otherwise supported reads

- **Client:** `slug`, host/device identifiers, serial/UUID, public key, timestamps,
  last-seen network/location/version/user-agent fields, and all `last_attested_*`
  details. `verified_at` is reduced to the existing `verified` boolean.
- **Gateway:** Firezone IPs, public key, online status, gateway-token ID,
  last-seen network/location/version/user-agent fields and `rotated_at` are not
  state attributes. The provider does inspect `rotated_at` for rotation warnings.
- **Actor:** directory provenance and inserted/updated/last-seen timestamps.
- **Group:** email, entity type, IdP ID and sync/inserted/updated timestamps.
  The group data source exposes `directory_id`; the managed resource only owns name.
- **Existing auth-provider data sources:** account ID, context, client/portal
  session lifetimes, and inserted/updated timestamps.
- **Directory data sources:** account/status/error/sync/timestamp fields and
  provider-specific settings: Google impersonation email, group sync mode and
  org-unit sync; Entra email field and sync-all-groups; Okta client ID and key ID.

These are read-model omissions, distinct from missing write functionality. Live
telemetry is generally lower priority than configuration and stable IDs.

## Recommended follow-up order

1. Policy `postures`, alongside posture-provider and X.509 lookup data sources.
2. Client-token lifecycle for service accounts; Resource/Policy/Gateway lookups.
3. Client verification and adoption-based name/slug management; site token lifecycle.
4. Remaining read fields, inventories, and operational actions where a clear
   Terraform ownership model exists.

Webhook receivers, flow-log ingestion, OAuth discovery, OpenAPI/Swagger and MCP
transport routes are integration/protocol surfaces rather than Terraform-managed
objects. They were checked in the router but are not counted as missing resources.
