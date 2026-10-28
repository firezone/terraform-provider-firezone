package provider_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"

	firezone "github.com/firezone/terraform-provider-firezone/pkg/provider"
)

func TestNew(t *testing.T) {
	ctx := context.Background()
	p := firezone.New("test")()

	var meta provider.MetadataResponse
	p.Metadata(ctx, provider.MetadataRequest{}, &meta)
	if meta.TypeName != "firezone" {
		t.Errorf("TypeName = %q, want %q", meta.TypeName, "firezone")
	}

	var schema provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &schema)
	if schema.Diagnostics.HasError() {
		t.Fatalf("Schema returned errors: %v", schema.Diagnostics)
	}
	for _, attr := range []string{"endpoint", "token"} {
		if _, ok := schema.Schema.Attributes[attr]; !ok {
			t.Errorf("schema is missing attribute %q", attr)
		}
	}

	if len(p.Resources(ctx)) == 0 {
		t.Error("provider exposes no resources")
	}
	if len(p.DataSources(ctx)) == 0 {
		t.Error("provider exposes no data sources")
	}
}
