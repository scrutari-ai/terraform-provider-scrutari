package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// ─────────────────────────────────────────────────────────────────
// scrutari_delegated_zone acceptance tests (RFC-010 S4)
//
// Environment contract: TF_ACC=1, SCRUTARI_API_KEY=scru_test_* with
// zones:read + zones:write (+ domains:write for the refusal test),
// SCRUTARI_HOST.
//
// Two structural realities shape this suite:
//
//   1. A CI zone can never reach `active`: activation requires
//      publishing a real parent-zone TXT and real NS records, which
//      a hermetic test cannot do. Everything testable without DNS is
//      tested (registration contract, the one-time challenge, read
//      round-trip, import, offboarding, the zone_not_active refusal
//      for zone-backed domains). The `active`-path coverage
//      (name_servers fill, zone-backed domain happy path) requires
//      the staging fixture zone — see the TODO at the bottom.
//
//   2. Zone names are SINGLE-USE: offboarding tombstones the name
//      forever (§6 takeover guard), so every test mints a fresh
//      random zone. Re-using a fixture name across runs would 409
//      on the second run by design, not by bug.
// ─────────────────────────────────────────────────────────────────

// uniqueZone mints a three-label zone under the RFC 2606-reserved
// `.test` TLD, unique per invocation (see reality #2 above).
func uniqueZone(t *testing.T) string {
	t.Helper()
	clean := strings.ToLower(strings.NewReplacer("/", "-", "_", "-", " ", "-").Replace(t.Name()))
	return fmt.Sprintf("acc-%s-%d.tfacc-zone.test", clean, time.Now().UnixNano())
}

// TestAccScrutariDelegatedZone_basic — registration contract.
//
// The zone is born `pending_verification` carrying the one-time
// parent-zone challenge; name_servers must be EMPTY (provisioning
// happens only after the TXT proof verifies — returning name servers
// before proof would invite publishing a delegation for a zone the
// caller hasn't proven control of).
func TestAccScrutariDelegatedZone_basic(t *testing.T) {
	zone := uniqueZone(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckZoneOffboarded,
		Steps: []resource.TestStep{
			{
				Config: testAccZoneConfig_basic(zone),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_delegated_zone.test", "zone", zone),
					resource.TestCheckResourceAttr("scrutari_delegated_zone.test", "status", "pending_verification"),
					resource.TestCheckResourceAttr("scrutari_delegated_zone.test", "delegation_mode", "ns"),
					resource.TestCheckResourceAttr("scrutari_delegated_zone.test", "challenge_host", "_scrutari-zone-challenge."+zone),
					resource.TestCheckResourceAttr("scrutari_delegated_zone.test", "name_servers.#", "0"),

					// The one-time proof token must land in state on
					// create — it can never be re-read.
					resource.TestCheckResourceAttrSet("scrutari_delegated_zone.test", "expected_txt"),

					resource.TestCheckResourceAttrSet("scrutari_delegated_zone.test", "id"),
					resource.TestCheckResourceAttrSet("scrutari_delegated_zone.test", "created_at"),
				),
			},
		},
	})
}

// TestAccScrutariDelegatedZone_import — import by UUID.
//
// `expected_txt` and `challenge_host` are create-only (reads never
// return the secret, and the challenge host travels with it), so
// they legitimately differ post-import; everything else must
// round-trip exactly.
func TestAccScrutariDelegatedZone_import(t *testing.T) {
	zone := uniqueZone(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckZoneOffboarded,
		Steps: []resource.TestStep{
			{
				Config: testAccZoneConfig_basic(zone),
			},
			{
				ResourceName:            "scrutari_delegated_zone.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"expected_txt", "challenge_host"},
			},
		},
	})
}

// TestAccScrutariDelegatedZone_apexRefused — the admission gate's
// three-label minimum surfaces at plan time via zoneRegex, before
// any API call.
func TestAccScrutariDelegatedZone_apexRefused(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccZoneConfig_basic("example.test"),
				ExpectError: regexp.MustCompile(`(?i)three labels`),
			},
		},
	})
}

// TestAccScrutariDelegatedZone_overlapRefused — the §6
// takeover-prevention overlap walk: a zone strictly inside an
// already-registered zone must 409, and the apply must fail loudly.
// Registering the parent zone first in the same config (depends_on
// forces ordering) makes the test hermetic.
func TestAccScrutariDelegatedZone_overlapRefused(t *testing.T) {
	parent := uniqueZone(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckZoneOffboarded,
		Steps: []resource.TestStep{
			{
				Config:      testAccZoneConfig_overlap(parent),
				ExpectError: regexp.MustCompile(`(?i)zone_unavailable|conflict|409`),
			},
		},
	})
}

// TestAccScrutariDelegatedZone_zoneBackedDomainRefused — a hostname
// under a zone that is NOT yet `active` must be refused with the
// gateway's zone_not_active validation error. This is the deepest
// API-level zone-backed assertion a hermetic suite can make (see
// reality #1 in the file header).
func TestAccScrutariDelegatedZone_zoneBackedDomainRefused(t *testing.T) {
	zone := uniqueZone(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckZoneOffboarded,
		Steps: []resource.TestStep{
			{
				Config:      testAccZoneConfig_withDomain(zone),
				ExpectError: regexp.MustCompile(`(?i)zone_not_active|not 'active'`),
			},
		},
	})
}

// ─────────────────────────────────────────────────────────────────
// CheckDestroy — zones do NOT 404 after destroy, by design.
//
// Terraform destroy starts the offboarding drain; the row remains
// readable as it walks offboarding → revoked (the tombstone that
// quarantines the name forever). So the gateway-side assertion is
// NOT "404" (that would mean the tombstone got deleted — a §6
// violation!) but "status is offboarding or revoked".
// ─────────────────────────────────────────────────────────────────

func testAccCheckZoneOffboarded(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "scrutari_delegated_zone" {
			continue
		}
		if rs.Primary.ID == "" {
			continue
		}

		status, body, err := testAccAPIGet("/v1/zones/" + rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("verify-offboard GET failed for zone %s: %w", rs.Primary.ID, err)
		}
		if status == http.StatusNotFound {
			return fmt.Errorf(
				"zone %s answered 404 after destroy — the tombstone row should NEVER be deleted "+
					"(RFC-010 §6 quarantine). Gateway-side regression. body=%s",
				rs.Primary.ID, body,
			)
		}
		if status != http.StatusOK {
			return fmt.Errorf("verify-offboard GET for zone %s returned HTTP %d: %s", rs.Primary.ID, status, body)
		}

		var row struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(body), &row); err != nil {
			return fmt.Errorf("verify-offboard parse for zone %s: %w (body=%s)", rs.Primary.ID, err, body)
		}
		if row.Status != "offboarding" && row.Status != "revoked" {
			return fmt.Errorf(
				"zone %s is %q after Terraform destroy; expected offboarding or revoked — "+
					"the destroy did not start the drain",
				rs.Primary.ID, row.Status,
			)
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────
// HCL fixtures
// ─────────────────────────────────────────────────────────────────

func testAccZoneConfig_basic(zone string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_delegated_zone" "test" {
  zone = %[1]q
}
`, zone)
}

// Parent + a child strictly inside it. depends_on forces the parent
// to register first so the child deterministically trips the
// overlap walk (without it, apply order is nondeterministic and the
// test would flake between "child refused" and "parent refused").
func testAccZoneConfig_overlap(parent string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_delegated_zone" "parent" {
  zone = %[1]q
}

resource "scrutari_delegated_zone" "child" {
  zone       = "inner.%[1]s"
  depends_on = [scrutari_delegated_zone.parent]
}
`, parent)
}

// A zone plus a hostname under it, wired by reference. The zone is
// born pending_verification, so the domain create must fail with
// zone_not_active.
func testAccZoneConfig_withDomain(zone string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_delegated_zone" "test" {
  zone = %[1]q
}

resource "scrutari_domain" "under_zone" {
  domain  = "api.%[1]s"
  zone_id = scrutari_delegated_zone.test.id
}
`, zone)
}

// ─────────────────────────────────────────────────────────────────
// TODO follow-up coverage — requires the staging fixture zone.
//
// The CI tenant needs ONE long-lived delegated zone held `active`
// against a real DNS name we control (e.g. tfacc.scrutari-ci.dev
// with its TXT + NS records published once, by hand, at fixture
// setup). With it:
//
//   - zone-backed domain happy path: born verified, no challenge,
//     destroy leaves the zone intact;
//   - name_servers.# > 0 after activation, activated_at set;
//   - depth rule: a.b.<zone> refused (domain_depth_exceeded), apex
//     refused (zone_apex_not_coverable — lifts when OPEN_TASKS #430
//     ships multi-SAN orders);
//   - import of a zone-backed domain (zone_id stays null until the
//     read API exposes delegated_zone_id — flip ImportStateVerify
//     expectations when that lands).
//
// The fixture zone must NEVER be offboarded by tests: its name is
// single-use (§6 tombstone). CheckDestroy for the happy-path suite
// asserts on the DOMAINS only.
// ─────────────────────────────────────────────────────────────────
