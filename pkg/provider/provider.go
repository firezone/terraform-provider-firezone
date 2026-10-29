// Package provider exposes the Firezone Terraform provider as a Go
// library, for tools that run it in-process instead of as a plugin
// binary, such as Upjet-based Crossplane providers
// (https://github.com/crossplane/upjet). Terraform itself does not use
// this package; it talks to the provider binary built from main.go.
//
// This is the only supported import path into the provider. Everything
// under internal/ remains private.
package provider

import (
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"

	internalprovider "github.com/firezone/terraform-provider-firezone/internal/provider"
)

// New returns a factory for the Firezone provider, matching the shape
// providerserver.Serve expects. Call the factory for an instance, e.g.
// for Upjet's config.WithTerraformPluginFrameworkProvider:
//
//	config.WithTerraformPluginFrameworkProvider(provider.New(version)())
//
// The version is reported in the API client's User-Agent header.
func New(version string) func() fwprovider.Provider {
	return internalprovider.New(version)
}
