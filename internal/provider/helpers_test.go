package provider

import (
	"context"
	"testing"
)

// TestStringListToModel pins that a missing API array reads back as an
// empty list, not null, so configs can iterate over it unguarded.
func TestStringListToModel(t *testing.T) {
	ctx := context.Background()

	empty, diags := stringListToModel(ctx, nil)
	if diags.HasError() {
		t.Fatalf("stringListToModel returned diagnostics: %v", diags)
	}
	if empty.IsNull() || len(empty.Elements()) != 0 {
		t.Errorf("nil input = %v, want an empty list", empty)
	}

	list, diags := stringListToModel(ctx, []string{"a", "b"})
	if diags.HasError() {
		t.Fatalf("stringListToModel returned diagnostics: %v", diags)
	}
	if got := len(list.Elements()); got != 2 {
		t.Errorf("got %d elements, want 2", got)
	}
}
