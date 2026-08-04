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

// newClientsListServer stands up an httptest server serving the given
// Clients from GET /clients, one page per call, and records the query
// string of every request so tests can assert the filters actually
// reached the wire.
func newClientsListServer(t *testing.T, pages [][]map[string]any, queries *[]string) *firezone.Client {
	t.Helper()

	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clients" {
			t.Errorf("unexpected request path %q, want /clients", r.URL.Path)
		}
		if queries != nil {
			*queries = append(*queries, r.URL.Query().Encode())
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

// TestFindClientDevices_SendsFilters checks the lookup pushes its
// filter to the API instead of scanning. The clients endpoint gained
// name and firezone_id filters precisely so a data source read costs one
// request rather than one per page of the account's devices.
func TestFindClientDevices_SendsFilters(t *testing.T) {
	tests := []struct {
		name      string
		opts      firezone.ClientListOptions
		wantQuery string
	}{
		{
			name:      "by name",
			opts:      firezone.ClientListOptions{Name: "jane-laptop"},
			wantQuery: "limit=100&name=jane-laptop",
		},
		{
			name:      "by firezone_id",
			opts:      firezone.ClientListOptions{FirezoneID: "fz-1"},
			wantQuery: "firezone_id=fz-1&limit=100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var queries []string
			pages := [][]map[string]any{{{"id": "client-1", "name": "jane-laptop", "firezone_id": "fz-1"}}}
			client := newClientsListServer(t, pages, &queries)

			matches, err := findClientDevices(context.Background(), client, tt.opts)
			if err != nil {
				t.Fatalf("findClientDevices returned error: %v", err)
			}
			if len(matches) != 1 {
				t.Fatalf("got %d matches, want 1", len(matches))
			}
			if len(queries) != 1 {
				t.Fatalf("made %d requests, want 1 (%v)", len(queries), queries)
			}
			if queries[0] != tt.wantQuery {
				t.Errorf("query = %q, want %q", queries[0], tt.wantQuery)
			}
		})
	}
}

// TestFindClientDevices_Paginates covers the case the filter doesn't
// remove: neither name nor firezone_id is unique, so matches can still
// span pages and every one must be collected for the ambiguity check.
func TestFindClientDevices_Paginates(t *testing.T) {
	pages := [][]map[string]any{
		{{"id": "client-1", "name": "shared-name"}},
		{{"id": "client-2", "name": "shared-name"}},
	}

	var queries []string
	client := newClientsListServer(t, pages, &queries)

	matches, err := findClientDevices(context.Background(), client,
		firezone.ClientListOptions{Name: "shared-name"})
	if err != nil {
		t.Fatalf("findClientDevices returned error: %v", err)
	}

	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2 (%v)", len(matches), matches)
	}
	if matches[0].ID != "client-1" || matches[1].ID != "client-2" {
		t.Errorf("matches = %v, want client-1 then client-2", matches)
	}
	// The second request must carry the cursor and keep the filter.
	if len(queries) != 2 {
		t.Fatalf("made %d requests, want 2 (%v)", len(queries), queries)
	}
	if queries[1] != "limit=100&name=shared-name&page_cursor=cursor-1" {
		t.Errorf("second query = %q, want the cursor and the filter", queries[1])
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
