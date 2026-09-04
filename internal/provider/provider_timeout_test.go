package provider

import (
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestResolveRequestTimeout pins the config-then-environment precedence
// and, in particular, that zero is a value rather than a synonym for
// unset - the API client reads it as "impose no timeout", so collapsing
// it into the not-configured case would silently reinstate the default
// a user asked to remove.
func TestResolveRequestTimeout(t *testing.T) {
	tests := []struct {
		name       string
		configured types.Int64
		env        string
		want       time.Duration
		wantOK     bool
		wantErr    bool
	}{
		{name: "config wins", configured: types.Int64Value(5), env: "60", want: 5 * time.Second, wantOK: true},
		{name: "zero from config disables the timeout", configured: types.Int64Value(0), want: 0, wantOK: true},
		{name: "falls back to env", configured: types.Int64Null(), env: "60", want: 60 * time.Second, wantOK: true},
		{name: "zero from env disables the timeout", configured: types.Int64Null(), env: "0", want: 0, wantOK: true},
		{name: "unset leaves the client default", configured: types.Int64Null(), wantOK: false},
		{name: "unknown leaves the client default", configured: types.Int64Unknown(), wantOK: false},
		{name: "negative env is an error", configured: types.Int64Null(), env: "-1", wantErr: true},
		{name: "non-numeric env is an error", configured: types.Int64Null(), env: "soon", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("FIREZONE_REQUEST_TIMEOUT_SECONDS", tt.env)
			} else {
				t.Setenv("FIREZONE_REQUEST_TIMEOUT_SECONDS", "")
			}

			var diags diag.Diagnostics
			got, ok := resolveRequestTimeout(tt.configured, &diags)

			if diags.HasError() != tt.wantErr {
				t.Fatalf("HasError() = %v, want %v (diags: %v)", diags.HasError(), tt.wantErr, diags)
			}
			if tt.wantErr {
				return
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
