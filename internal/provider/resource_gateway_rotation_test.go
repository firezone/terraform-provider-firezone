package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestRotationTriggered covers when a token_rotation_trigger change
// should rotate. Getting this wrong is expensive in both directions: a
// false negative silently skips a scheduled rotation, and a false
// positive burns a token on every apply.
func TestRotationTriggered(t *testing.T) {
	tests := []struct {
		name    string
		planned types.String
		stored  types.String
		want    bool
	}{
		{
			name:    "unchanged value does not rotate",
			planned: types.StringValue("v1"),
			stored:  types.StringValue("v1"),
			want:    false,
		},
		{
			name:    "changed value rotates",
			planned: types.StringValue("v2"),
			stored:  types.StringValue("v1"),
			want:    true,
		},
		{
			// The common case: never set, still not set. A gateway whose
			// config omits the trigger must never rotate on apply.
			name:    "null to null does not rotate",
			planned: types.StringNull(),
			stored:  types.StringNull(),
			want:    false,
		},
		{
			name:    "adding a trigger rotates",
			planned: types.StringValue("v1"),
			stored:  types.StringNull(),
			want:    true,
		},
		{
			// Removing the trigger is a config change, not a request to
			// rotate - but it does have to be treated consistently, and
			// rotating once on removal is the safer reading than
			// silently diverging from state.
			name:    "removing the trigger rotates",
			planned: types.StringNull(),
			stored:  types.StringValue("v1"),
			want:    true,
		},
		{
			// time_rotating.<name>.id is unknown until apply. It is only
			// unknown because something upstream is changing it, so this
			// must count as a rotation or scheduled rotation never fires.
			name:    "unknown planned value rotates",
			planned: types.StringUnknown(),
			stored:  types.StringValue("v1"),
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rotationTriggered(tt.planned, tt.stored); got != tt.want {
				t.Errorf("rotationTriggered(%v, %v) = %v, want %v",
					tt.planned, tt.stored, got, tt.want)
			}
		})
	}
}
