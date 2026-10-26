package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	firezone "github.com/firezone/firezone-sdk-go"
)

// TestListGroupMembers_Paginates checks every page of members is
// collected, with the email each one carries.
func TestListGroupMembers_Paginates(t *testing.T) {
	pages := [][]map[string]any{
		{{"id": "actor-1", "name": "Jane", "email": "jane@example.com", "type": "account_user"}},
		{{"id": "actor-2", "name": "Bot", "type": "service_account"}},
	}

	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/groups/group-1/memberships" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		if served > 0 && r.URL.Query().Get("page_cursor") != fmt.Sprintf("cursor-%d", served) {
			t.Errorf("request %d did not carry the cursor: %q", served, r.URL.RawQuery)
		}
		data := pages[served]
		served++
		nextPage := ""
		if served < len(pages) {
			nextPage = fmt.Sprintf("cursor-%d", served)
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

	members, err := listGroupMembers(context.Background(), client, "group-1")
	if err != nil {
		t.Fatalf("listGroupMembers returned error: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2 (%v)", len(members), members)
	}
	if members[0].Email != "jane@example.com" || members[1].Email != "" {
		t.Errorf("emails = %q, %q; want jane@example.com and empty", members[0].Email, members[1].Email)
	}
}

// TestAccGroupMembersDataSource adds an Actor to a new Group and reads
// it back with its email. It also reads the managed Everyone Group,
// whose members Firezone sets itself.
func TestAccGroupMembersDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "firezone_group" "test" {
  name = "acc-test-ds-group-members"
}

resource "firezone_actor" "test" {
  name  = "acc-test-ds-group-member"
  type  = "account_user"
  email = "acc-test-ds-group-member@example.com"
}

resource "firezone_group_membership" "test" {
  group_id = firezone_group.test.id
  actor_id = firezone_actor.test.id
}

data "firezone_group_members" "test" {
  group_id = firezone_group_membership.test.group_id
}

data "firezone_group" "everyone" {
  name = "Everyone"
}

data "firezone_group_members" "everyone" {
  group_id   = data.firezone_group.everyone.id
  depends_on = [firezone_actor.test]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.firezone_group_members.test", "members.#", "1"),
					resource.TestCheckResourceAttrPair("data.firezone_group_members.test", "members.0.id", "firezone_actor.test", "id"),
					resource.TestCheckResourceAttr("data.firezone_group_members.test", "members.0.email", "acc-test-ds-group-member@example.com"),
					resource.TestCheckResourceAttr("data.firezone_group.everyone", "type", "managed"),
					resource.TestCheckTypeSetElemNestedAttrs("data.firezone_group_members.everyone", "members.*",
						map[string]string{"email": "acc-test-ds-group-member@example.com"}),
				),
			},
		},
	})
}
