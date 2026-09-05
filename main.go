// Command terraform-provider-firezone is the Terraform provider for
// Firezone (https://www.firezone.dev).
package main

//go:generate go tool tfplugindocs generate

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/firezone/terraform-provider-firezone/internal/provider"
)

// version is overwritten at release time by GoReleaser's ldflags
// (-X main.version=...). Local and `go install` builds keep "dev",
// which is what the User-Agent then reports.
var version = "dev"

func main() {
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/firezone/firezone",
	})
	if err != nil {
		log.Fatal(err)
	}
}
