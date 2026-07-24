package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// testAccProtoV6ProviderFactories is shared by every acceptance test in
// this package.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"firezone": providerserver.NewProtocol6WithError(New()()),
}

// testAccPreCheck validates that acceptance tests have a real Firezone
// dev server and API token to run against.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("FIREZONE_ENDPOINT") == "" {
		t.Fatal("FIREZONE_ENDPOINT must be set for acceptance tests")
	}
	if os.Getenv("FIREZONE_TOKEN") == "" {
		t.Fatal("FIREZONE_TOKEN must be set for acceptance tests")
	}
}
