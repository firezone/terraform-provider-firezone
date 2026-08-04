package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	firezone "github.com/firezone/firezone-go"
)

// fwDiagnostics is a local alias for diag.Diagnostics, used to shorten
// helper function signatures throughout this package.
type fwDiagnostics = diag.Diagnostics

// idPath is path.Root("id"), the import target for every resource in
// this provider whose Terraform ID is a plain API ID (as opposed to a
// composite one - see resource_group_membership.go and
// resource_gateway.go for those).
var idPath = path.Root("id")

// groupIDPath and actorIDPath are used by
// firezone_group_membership.ImportState to set its two composite-ID
// components individually.
var (
	groupIDPath = path.Root("group_id")
	actorIDPath = path.Root("actor_id")
)

// siteIDPath is used by firezone_gateway.ImportState to set its
// composite ID's site_id component.
var siteIDPath = path.Root("site_id")

// resourceIDPath and deviceIDPath are used by
// firezone_pool_member.ImportState to set its two composite-ID
// components individually.
var (
	resourceIDPath = path.Root("resource_id")
	deviceIDPath   = path.Root("device_id")
)

// ipStackPath is used by firezone_resource.ValidateConfig to attribute
// its type-conditional ip_stack error.
var ipStackPath = path.Root("ip_stack")

// configureClient extracts the shared *firezone.Client from
// providerData (resource.ConfigureRequest.ProviderData or
// datasource.ConfigureRequest.ProviderData), appending a diagnostic and
// returning ok=false if it's missing or the wrong type. providerData is
// nil during the initial "terraform validate" pass before Configure has
// run on the provider itself - callers should treat that as a no-op,
// not an error, which this helper does by returning ok=false with no
// diagnostic appended.
func configureClient(providerData any, diags *fwDiagnostics) (*firezone.Client, bool) {
	if providerData == nil {
		return nil, false
	}
	client, ok := providerData.(*firezone.Client)
	if !ok {
		diags.AddError(
			"Unexpected Provider Data Type",
			fmt.Sprintf("Expected *firezone.Client, got: %T. This is a provider bug - please report it.", providerData),
		)
		return nil, false
	}
	return client, true
}
