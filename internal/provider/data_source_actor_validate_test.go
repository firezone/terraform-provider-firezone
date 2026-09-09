package provider

import "testing"

// TestActorFilterDescription covers the message text behind the
// not-found and ambiguous diagnostics. Email-only lookups used to
// render as `name "" with email = "..."`, which described a query the
// practitioner never wrote.
func TestActorFilterDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		actorName  string
		actorEmail string
		want       string
	}{
		{
			name:      "name only",
			actorName: "Ada Lovelace",
			want:      `name "Ada Lovelace"`,
		},
		{
			name:       "email only",
			actorEmail: "ada@example.com",
			want:       `email "ada@example.com"`,
		},
		{
			name:       "both",
			actorName:  "Ada Lovelace",
			actorEmail: "ada@example.com",
			want:       `name "Ada Lovelace" and email "ada@example.com"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := actorFilterDescription(tt.actorName, tt.actorEmail)
			if got != tt.want {
				t.Errorf("actorFilterDescription(%q, %q) = %q, want %q",
					tt.actorName, tt.actorEmail, got, tt.want)
			}
		})
	}
}
