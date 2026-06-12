package provider

import (
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
// scrutari_domain acceptance tests (RFC-010 S4)
//
// Environment contract: same as the routes suite (TF_ACC=1,
// SCRUTARI_API_KEY=scru_test_* with domains:read + domains:write
// scopes, SCRUTARI_HOST). Classic-mode tests need NO pre-seeded
// fixtures: a domain create is just a challenge row — no DNS has to
// exist, the row simply stays `pending`. That makes this suite
// fully self-contained.
//
// Zone-BACKED domain coverage lives in the delegated-zone suite
// (TestAccScrutariDelegatedZone_zoneBackedDomainRefused): without a
// real parent-zone TXT publication a CI zone can never reach
// `active`, so the meaningful API-level assertion is the
// zone_not_active refusal. Full happy-path zone-backed coverage
// needs the staging fixture zone — see the suite-level TODO at the
// bottom of delegated_zone_acceptance_test.go.
// ─────────────────────────────────────────────────────────────────

// uniqueDomain returns a hostname unique to this test invocation,
// under the RFC 2606-reserved `.test` TLD. Uniqueness matters less
// than for routes (re-POSTing an existing domain just rotates its
// challenge) but keeps CheckDestroy assertions unambiguous across
// interrupted runs.
func uniqueDomain(t *testing.T) string {
	t.Helper()
	clean := strings.ToLower(strings.NewReplacer("/", "-", "_", "-", " ", "-").Replace(t.Name()))
	return fmt.Sprintf("acc-%s-%d.tfacc.test", clean, time.Now().UnixNano())
}

// TestAccScrutariDomain_basic — classic-mode CRUD lifecycle.
//
// Asserts the one-time challenge contract: the create response must
// populate `challenge_host` + `expected_value` (the TXT pair the
// caller publishes) and the row must be born `pending`.
func TestAccScrutariDomain_basic(t *testing.T) {
	domain := uniqueDomain(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDomainDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccDomainConfig_basic(domain),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_domain.test", "domain", domain),
					resource.TestCheckResourceAttr("scrutari_domain.test", "status", "pending"),
					resource.TestCheckResourceAttr("scrutari_domain.test", "verification_type", "txt"),
					resource.TestCheckResourceAttr("scrutari_domain.test", "challenge_host", "_scrutari-challenge."+domain),

					// The one-time secret must land in state on
					// create — it can never be re-read, so a miss
					// here is unrecoverable for the caller.
					resource.TestCheckResourceAttrSet("scrutari_domain.test", "expected_value"),

					resource.TestCheckResourceAttrSet("scrutari_domain.test", "id"),
					resource.TestCheckResourceAttrSet("scrutari_domain.test", "tenant_id"),
					resource.TestCheckResourceAttrSet("scrutari_domain.test", "created_at"),
				),
			},
		},
	})
}

// TestAccScrutariDomain_import — `terraform import` reconciliation.
//
// `expected_value` is create-only (the read API never returns it) and
// `zone_id` is config-only (the read shape doesn't expose
// delegated_zone_id yet) — both are EXPECTED to differ between the
// created state and the imported state, hence the
// ImportStateVerifyIgnore. Everything else must round-trip exactly;
// drift there means the READ hydration dropped a field.
func TestAccScrutariDomain_import(t *testing.T) {
	domain := uniqueDomain(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDomainDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccDomainConfig_basic(domain),
			},
			{
				ResourceName:            "scrutari_domain.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"expected_value", "zone_id"},
			},
		},
	})
}

// TestAccScrutariDomain_invalidSyntax — plan-time validation.
//
// The fqdnRegex validator must refuse junk before any API
// round-trip; the error is a plan-time diagnostic, so no resource is
// ever created (CheckDestroy stays trivially green).
func TestAccScrutariDomain_invalidSyntax(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccDomainConfig_basic("https://not-a-hostname.test/path"),
				ExpectError: regexp.MustCompile(`(?i)fully-qualified hostname`),
			},
		},
	})
}

// TestAccScrutariDomain_zoneIdMustBeUuid — plan-time validation on
// the zone_id reference shape.
func TestAccScrutariDomain_zoneIdMustBeUuid(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccDomainConfig_zoneBacked(uniqueDomain(t), "not-a-uuid"),
				ExpectError: regexp.MustCompile(`(?i)zone UUID`),
			},
		},
	})
}

// ─────────────────────────────────────────────────────────────────
// CheckDestroy — gateway-side verification, same rationale as the
// routes suite: Terraform state saying "gone" is not the assertion
// that matters; the gateway answering 404 is.
// ─────────────────────────────────────────────────────────────────

func testAccCheckDomainDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "scrutari_domain" {
			continue
		}
		if rs.Primary.ID == "" {
			continue
		}

		status, body, err := testAccAPIGet("/v1/domains/" + rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("verify-destroy GET failed for domain %s: %w", rs.Primary.ID, err)
		}
		if status != http.StatusNotFound {
			return fmt.Errorf(
				"domain %s still on gateway after Terraform destroy (HTTP %d). body=%s",
				rs.Primary.ID, status, body,
			)
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────
// HCL fixtures
// ─────────────────────────────────────────────────────────────────

func testAccDomainConfig_basic(domain string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_domain" "test" {
  domain = %[1]q
}
`, domain)
}

func testAccDomainConfig_zoneBacked(domain, zoneID string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_domain" "test" {
  domain  = %[1]q
  zone_id = %[2]q
}
`, domain, zoneID)
}
