package provider

import (
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestPoolMemberID covers the composite ID round-trip. This is a plain
// unit test rather than an acceptance test because it needs neither a
// server nor a terraform binary - and unlike the acceptance tests
// below, it can always run (see testAccPoolMemberPreCheck).
func TestPoolMemberID(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		const (
			wantResourceID = "42a7f82f-831a-4a9d-8f17-c66c2bb6e205"
			wantDeviceID   = "cc9f561a-444d-4083-ab38-0abc6cf2314c"
		)

		id := poolMemberID(wantResourceID, wantDeviceID)

		resourceID, deviceID, err := parsePoolMemberID(id)
		if err != nil {
			t.Fatalf("parsePoolMemberID(%q) returned error: %v", id, err)
		}
		if resourceID != wantResourceID {
			t.Errorf("resourceID = %q, want %q", resourceID, wantResourceID)
		}
		if deviceID != wantDeviceID {
			t.Errorf("deviceID = %q, want %q", deviceID, wantDeviceID)
		}
	})

	invalid := []struct {
		name string
		id   string
	}{
		{name: "no separator", id: "42a7f82f-831a-4a9d-8f17-c66c2bb6e205"},
		{name: "empty", id: ""},
		{name: "missing resource_id", id: "/cc9f561a-444d-4083-ab38-0abc6cf2314c"},
		{name: "missing device_id", id: "42a7f82f-831a-4a9d-8f17-c66c2bb6e205/"},
		{name: "separator only", id: "/"},
	}

	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parsePoolMemberID(tt.id); err == nil {
				t.Errorf("parsePoolMemberID(%q) returned nil error, want one", tt.id)
			}
		})
	}
}

// poolMemberTestClientIDs returns the two Client IDs the pool-member
// acceptance tests run against, either of which may be empty.
//
// Unlike every other acceptance test in this package, these can't build
// their own fixtures: Clients register themselves when they first
// connect, so neither Terraform nor the REST API can create one. The
// tests have to be pointed at Clients that already exist in the dev
// account.
//
// This deliberately does not fail or skip - it's called while building
// the TestCase, which happens even when TF_ACC is unset and the case
// never runs. Gating belongs in testAccPoolMemberPreCheck.
func poolMemberTestClientIDs() (string, string) {
	return os.Getenv("FIREZONE_TEST_CLIENT_ID"), os.Getenv("FIREZONE_TEST_CLIENT_ID_2")
}

// testAccPoolMemberPreCheck extends testAccPreCheck with the two Client
// IDs these tests need. Runs inside TestCase.PreCheck, so it only fires
// once TF_ACC has already opted the case in.
func testAccPoolMemberPreCheck(t *testing.T) {
	t.Helper()
	testAccPreCheck(t)

	first, second := poolMemberTestClientIDs()
	if first == "" || second == "" {
		t.Skip("FIREZONE_TEST_CLIENT_ID and FIREZONE_TEST_CLIENT_ID_2 must name two " +
			"existing Clients in the dev account; skipping")
	}
}

func TestAccPoolMemberResource(t *testing.T) {
	clientID, _ := poolMemberTestClientIDs()

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPoolMemberPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPoolMemberResourceConfig(clientID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(
						"firezone_pool_member.test", "resource_id", "firezone_resource.pool", "id"),
					resource.TestCheckResourceAttr(
						"firezone_pool_member.test", "device_id", clientID),
				),
			},
			{
				ResourceName:      "firezone_pool_member.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccPoolMemberResource_Additive verifies the load-bearing design
// decision from resource_pool_member.go: two pool member resources
// against the same pool must both survive independently across an
// apply, because Create/Delete use the PATCH add/remove endpoint rather
// than the PUT replace-all endpoint.
func TestAccPoolMemberResource_Additive(t *testing.T) {
	clientID, otherClientID := poolMemberTestClientIDs()

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPoolMemberPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPoolMemberResourceAdditiveConfig(clientID, otherClientID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(
						"firezone_pool_member.one", "device_id", clientID),
					resource.TestCheckResourceAttr(
						"firezone_pool_member.two", "device_id", otherClientID),
				),
			},
			// Re-apply the same config: if Create ever regresses to PUT
			// replace-all, one of these two members would be gone and
			// this step's plan would show a diff.
			{
				Config:   testAccPoolMemberResourceAdditiveConfig(clientID, otherClientID),
				PlanOnly: true,
			},
		},
	})
}

// TestAccPoolMemberResource_NotAPool checks that pointing the resource
// at a non-pool Resource surfaces the API's 400 rather than silently
// succeeding.
func TestAccPoolMemberResource_NotAPool(t *testing.T) {
	clientID, _ := poolMemberTestClientIDs()

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPoolMemberPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccPoolMemberResourceNotAPoolConfig(clientID),
				ExpectError: regexp.MustCompile(`has no pool members`),
			},
		},
	})
}

func testAccPoolMemberResourceConfig(clientID string) string {
	return `
resource "firezone_resource" "pool" {
  name = "acc-test-pool"
  type = "static_device_pool"
}

resource "firezone_pool_member" "test" {
  resource_id = firezone_resource.pool.id
  device_id   = "` + clientID + `"
}
`
}

func testAccPoolMemberResourceAdditiveConfig(clientID, otherClientID string) string {
	return `
resource "firezone_resource" "pool" {
  name = "acc-test-pool-additive"
  type = "static_device_pool"
}

resource "firezone_pool_member" "one" {
  resource_id = firezone_resource.pool.id
  device_id   = "` + clientID + `"
}

resource "firezone_pool_member" "two" {
  resource_id = firezone_resource.pool.id
  device_id   = "` + otherClientID + `"
}
`
}

func testAccPoolMemberResourceNotAPoolConfig(clientID string) string {
	return `
resource "firezone_site" "test" {
  name = "acc-test-site-pool"
}

resource "firezone_resource" "not_a_pool" {
  site_id = firezone_site.test.id
  name    = "acc-test-not-a-pool"
  type    = "dns"
  address = "internal.example.com"
}

resource "firezone_pool_member" "test" {
  resource_id = firezone_resource.not_a_pool.id
  device_id   = "` + clientID + `"
}
`
}
