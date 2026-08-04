package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	firezone "github.com/firezone/firezone-go"
)

// newClientsListServer stands up an httptest server that serves the
// given Clients from GET /clients, one page per call, so the pagination
// loop in findClientDevices is actually exercised rather than assumed.
func newClientsListServer(t *testing.T, pages [][]map[string]any) *firezone.Client {
	t.Helper()

	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clients" {
			t.Errorf("unexpected request path %q, want /clients", r.URL.Path)
		}

		data := []map[string]any{}
		nextPage := ""
		if served < len(pages) {
			data = pages[served]
			served++
			if served < len(pages) {
				nextPage = fmt.Sprintf("cursor-%d", served)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":     data,
			"metadata": map[string]any{"limit": 100, "next_page": nextPage},
		})
	}))
	t.Cleanup(srv.Close)

	client, err := firezone.NewClient(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	return client
}

func TestFindClientDevices(t *testing.T) {
	pages := [][]map[string]any{
		{
			{"id": "client-1", "name": "jane-laptop", "firezone_id": "fz-1"},
			{"id": "client-2", "name": "shared-name", "firezone_id": "fz-2"},
		},
		{
			{"id": "client-3", "name": "shared-name", "firezone_id": "fz-3"},
		},
	}

	tests := []struct {
		name    string
		match   func(firezone.ClientDevice) bool
		wantIDs []string
	}{
		{
			name:    "unique name",
			match:   func(c firezone.ClientDevice) bool { return c.Name == "jane-laptop" },
			wantIDs: []string{"client-1"},
		},
		{
			// Spans a page boundary: a matcher that stopped at the first
			// page would find only one of these two.
			name:    "duplicate name across pages",
			match:   func(c firezone.ClientDevice) bool { return c.Name == "shared-name" },
			wantIDs: []string{"client-2", "client-3"},
		},
		{
			name:    "firezone_id on the second page",
			match:   func(c firezone.ClientDevice) bool { return c.FirezoneID == "fz-3" },
			wantIDs: []string{"client-3"},
		},
		{
			name:    "no match",
			match:   func(c firezone.ClientDevice) bool { return c.Name == "nonexistent" },
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newClientsListServer(t, pages)

			matches, err := findClientDevices(context.Background(), client, tt.match)
			if err != nil {
				t.Fatalf("findClientDevices returned error: %v", err)
			}

			if len(matches) != len(tt.wantIDs) {
				t.Fatalf("got %d matches, want %d (%v)", len(matches), len(tt.wantIDs), matches)
			}
			for i, want := range tt.wantIDs {
				if matches[i].ID != want {
					t.Errorf("matches[%d].ID = %q, want %q", i, matches[i].ID, want)
				}
			}
		})
	}
}

func TestAccClientDataSource(t *testing.T) {
	clientID := os.Getenv("FIREZONE_TEST_CLIENT_ID")

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if clientID == "" {
				t.Skip("FIREZONE_TEST_CLIENT_ID must name an existing Client; skipping")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "firezone_client" "by_id" {
  id = "` + clientID + `"
}

data "firezone_client" "by_firezone_id" {
  firezone_id = data.firezone_client.by_id.firezone_id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.firezone_client.by_id", "id", clientID),
					// The firezone_id round-trip proves the full-scan
					// lookup path finds the same device the direct Get did.
					resource.TestCheckResourceAttrPair(
						"data.firezone_client.by_firezone_id", "id",
						"data.firezone_client.by_id", "id"),
				),
			},
		},
	})
}

// TestAccClientDataSource_ExactlyOneOf checks the ConfigValidator
// rejects both zero and multiple lookup keys at plan time.
func TestAccClientDataSource_ExactlyOneOf(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      `data "firezone_client" "test" {}`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
			{
				Config: `
data "firezone_client" "test" {
  id   = "42a7f82f-831a-4a9d-8f17-c66c2bb6e205"
  name = "jane-laptop"
}
`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
		},
	})
}
