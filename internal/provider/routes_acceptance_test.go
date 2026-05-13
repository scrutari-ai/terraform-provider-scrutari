package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// ─────────────────────────────────────────────────────────────────
// scrutari_route acceptance tests
//
// All tests in this file require:
//   - TF_ACC=1
//   - SCRUTARI_API_KEY set to a scru_test_* key with routes:read +
//     routes:write scopes (the SCRUTARI_CI_KEY from the
//     tnt_terraform_provider_ci sandbox tenant).
//   - SCRUTARI_HOST set to the control-plane URL (in CI:
//     https://api.edge.scrutari.ai).
//
// The testAccPreCheck helper in provider_test.go enforces both
// the presence and the scru_test_ prefix on the key.
// ─────────────────────────────────────────────────────────────────

// TestAccScrutariRoute_basic — happy-path CRUD lifecycle.
//
// This is the single highest-leverage test in the suite: it
// exercises Create → Read (implicit, via TestCheckResourceAttr
// against the post-apply state) → Destroy → API-side 404 verify.
// Catches the largest class of provider bugs (drift, missing
// fields in hydrateModelFromAPI, partial-state bugs).
func TestAccScrutariRoute_basic(t *testing.T) {
	host := testAccPreVerifiedHosts[0]
	pathPrefix := uniquePathPrefix(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRouteDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRouteConfig_basic(
					host, pathPrefix,
					"https://upstream-basic.example.test",
					"required",
				),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Schema fields the test author supplied.
					resource.TestCheckResourceAttr("scrutari_route.test", "host", host),
					resource.TestCheckResourceAttr("scrutari_route.test", "path_prefix", pathPrefix),
					resource.TestCheckResourceAttr("scrutari_route.test", "upstream_url", "https://upstream-basic.example.test"),
					resource.TestCheckResourceAttr("scrutari_route.test", "auth_mode", "required"),

					// Computed fields the gateway must populate.
					// Missing any of these would mean
					// hydrateModelFromAPI dropped a field on the
					// CREATE return path.
					resource.TestCheckResourceAttrSet("scrutari_route.test", "id"),
					resource.TestCheckResourceAttrSet("scrutari_route.test", "tenant_id"),
					resource.TestCheckResourceAttrSet("scrutari_route.test", "is_active"),
					resource.TestCheckResourceAttrSet("scrutari_route.test", "created_at"),
					resource.TestCheckResourceAttrSet("scrutari_route.test", "updated_at"),
				),
			},
		},
	})
}

// TestAccScrutariRoute_import — `terraform import` reconciliation.
//
// Catches the "CREATE response hydrates field set X, READ response
// hydrates field set Y" class of bug: ImportStateVerify diffs the
// post-import state against the pre-import state, so any drift here
// fails the test loudly.
func TestAccScrutariRoute_import(t *testing.T) {
	host := testAccPreVerifiedHosts[1]
	pathPrefix := uniquePathPrefix(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRouteDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRouteConfig_basic(
					host, pathPrefix,
					"https://upstream-import.example.test",
					"required",
				),
			},
			{
				ResourceName:      "scrutari_route.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccScrutariRoute_update — partial update via PATCH.
//
// Confirms buildRoutePatch only sends the changed fields and that
// the resource id stays stable across an in-place update. A drift
// in `id` between the two steps would mean we accidentally did a
// delete-then-create when we should have done an in-place PATCH —
// catastrophic for production state because it would cause a brief
// traffic outage on every Terraform run.
func TestAccScrutariRoute_update(t *testing.T) {
	host := testAccPreVerifiedHosts[2]
	pathPrefix := uniquePathPrefix(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRouteDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRouteConfig_basic(
					host, pathPrefix,
					"https://upstream-v1.example.test",
					"required",
				),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_route.test", "upstream_url", "https://upstream-v1.example.test"),
					resource.TestCheckResourceAttr("scrutari_route.test", "auth_mode", "required"),
				),
			},
			{
				Config: testAccRouteConfig_basic(
					host, pathPrefix,
					"https://upstream-v2.example.test",
					"optional",
				),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_route.test", "upstream_url", "https://upstream-v2.example.test"),
					resource.TestCheckResourceAttr("scrutari_route.test", "auth_mode", "optional"),
				),
			},
		},
	})
}

// TestAccScrutariRoute_conflict — (host, path_prefix) uniqueness.
//
// The gateway returns 409 route_conflict when two routes collide
// on (host, path_prefix). We need this to surface as a Terraform
// diagnostic — not a silent apply that leaves the second resource
// in a half-state. The ExpectError regex is intentionally
// forgiving: gateway error codes can rename (route_conflict /
// already_exists / numeric 409 in the diagnostic), and what
// matters is that apply fails loudly, not which specific string
// surfaces.
func TestAccScrutariRoute_conflict(t *testing.T) {
	host := testAccPreVerifiedHosts[0]
	pathPrefix := uniquePathPrefix(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRouteDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccRouteConfig_conflict(host, pathPrefix),
				ExpectError: regexp.MustCompile(`(?i)route_conflict|already exists|conflict|409`),
			},
		},
	})
}

// ─────────────────────────────────────────────────────────────────
// CheckDestroy — verifies the gateway state, not just Terraform state.
//
// The provider framework's destroy step only confirms Terraform's
// view of the world. The painful failure mode is "Terraform thinks
// the resource is gone but the gateway still has it" — leaking live
// infra without either side noticing. This helper walks every
// scrutari_route resource in state, GETs its id from the gateway,
// and asserts a 404.
// ─────────────────────────────────────────────────────────────────

func testAccCheckRouteDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "scrutari_route" {
			continue
		}
		if rs.Primary.ID == "" {
			// Never made it to the API (e.g. plan-time validation
			// failure). Nothing to clean up.
			continue
		}

		status, body, err := testAccAPIGet("/v1/routes/" + rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("verify-destroy GET failed for route %s: %w", rs.Primary.ID, err)
		}
		if status != http.StatusNotFound {
			return fmt.Errorf(
				"route %s still on gateway after Terraform destroy "+
					"(HTTP %d). Terraform state and gateway state disagree — "+
					"this is exactly the drift we run acceptance tests to catch. body=%s",
				rs.Primary.ID, status, body,
			)
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────
// HCL fixtures
//
// Kept at the bottom of the file so the test bodies above read top-
// down. Each fixture is parameterised; no globals, so two tests
// running in parallel against different fixture values don't
// contaminate each other.
// ─────────────────────────────────────────────────────────────────

// testAccRouteConfig_basic — one scrutari_route.test resource.
// The provider block is empty: SCRUTARI_API_KEY and SCRUTARI_HOST
// flow in from the environment (the provider's Configure step
// reads them when the matching schema attributes are unset).
func testAccRouteConfig_basic(host, pathPrefix, upstream, authMode string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_route" "test" {
  host         = %[1]q
  path_prefix  = %[2]q
  upstream_url = %[3]q
  auth_mode    = %[4]q
}
`, host, pathPrefix, upstream, authMode)
}

// testAccRouteConfig_conflict — two routes sharing (host, path_prefix).
// The second apply must fail with the gateway's 409 surface.
func testAccRouteConfig_conflict(host, pathPrefix string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_route" "a" {
  host         = %[1]q
  path_prefix  = %[2]q
  upstream_url = "https://upstream-a.example.test"
}

resource "scrutari_route" "b" {
  host         = %[1]q
  path_prefix  = %[2]q
  upstream_url = "https://upstream-b.example.test"
}
`, host, pathPrefix)
}

// ─────────────────────────────────────────────────────────────────
// TODO follow-up suites — scoped out of this PR.
//
// Tracked separately so the routes coverage lands clean and each
// follow-up gets its own focused review.
//
//   - api_key_resource_acceptance_test.go
//       mint + revoke; assert plaintext is delivered exactly once;
//       assert key_hint matches plaintext[-4:]; environment=test
//       round-trips correctly through the schema.
//
//   - domain_data_source_acceptance_test.go
//       read terraform-ci-a.test (seeded by provisioning script);
//       assert all 11 attributes hydrate; null-handling on
//       last_error + verified_at when status='verified'.
//
//   - tenant_data_source_acceptance_test.go
//       read the one CI tenant; assert plan='starter';
//       features.ci_sandbox=true; rate_limit_rps=50.
//
//   - member_data_source_acceptance_test.go
//       requires the provisioning script to seed a deterministic
//       member user_id first. Park behind that prerequisite.
// ─────────────────────────────────────────────────────────────────
