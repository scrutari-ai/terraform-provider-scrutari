package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// ─────────────────────────────────────────────────────────────────
// scrutari_transport_policy acceptance tests (RFC-012 S5c)
//
// Environment contract: TF_ACC=1, SCRUTARI_API_KEY=scru_test_* with
// transport_policy:read + transport_policy:write, SCRUTARI_HOST.
//
// Two structural realities shape this suite:
//
//   1. SINGLETON: one document per tenant, so these tests serialize
//      against each other and against any other suite that touches
//      the CI tenant's policy. The framework runs tests in one
//      process sequentially by default; do NOT add t.Parallel()
//      here.
//
//   2. The CI gateway runs the HybridOnly posture, which ATTESTS
//      `hybrid` and cannot attest `cnsa-2.0`. That makes both D2
//      arms testable hermetically: enforce+hybrid is accepted
//      (attestable_here = true) and enforce+cnsa-2.0 is refused
//      with transport_policy_not_attestable. The cnsa-2.0
//      enforce-mode HAPPY path needs the CNSA staging endpoint and
//      parks with the other posture-dependent coverage (see the
//      fixture TODO in delegated_zone_acceptance_test.go).
// ─────────────────────────────────────────────────────────────────

// TestAccScrutariTransportPolicy_lifecycle — set, in-place update,
// and the enforce+hybrid happy path on an attesting deployment.
func TestAccScrutariTransportPolicy_lifecycle(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTransportPolicyReset,
		Steps: []resource.TestStep{
			{
				Config: testAccTransportPolicyConfig("hybrid", "monitor"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "id", "tenant"),
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "min_posture", "hybrid"),
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "mode", "monitor"),
					// The CI deployment is HybridOnly: a hybrid floor
					// is provable, and the gateway must SAY so.
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "attestable_here", "true"),
					resource.TestCheckResourceAttrSet("scrutari_transport_policy.test", "updated_at"),
				),
			},
			{
				// In-place update: monitor -> enforce on an
				// attestable floor must be accepted (the D2 gate's
				// happy arm) and must NOT replace the resource.
				Config: testAccTransportPolicyConfig("hybrid", "enforce"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "mode", "enforce"),
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "attestable_here", "true"),
				),
			},
			{
				// And back down: design partners will flip between
				// monitor and enforce while tuning; the round-trip
				// must be clean.
				Config: testAccTransportPolicyConfig("any", "monitor"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "min_posture", "any"),
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "mode", "monitor"),
				),
			},
		},
	})
}

// TestAccScrutariTransportPolicy_import — singleton import via the
// literal `tenant` id. Every attribute is readable, so the imported
// state must match exactly (no VerifyIgnore).
func TestAccScrutariTransportPolicy_import(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTransportPolicyReset,
		Steps: []resource.TestStep{
			{
				Config: testAccTransportPolicyConfig("hybrid", "monitor"),
			},
			{
				ResourceName:      "scrutari_transport_policy.test",
				ImportState:       true,
				ImportStateId:     "tenant",
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccScrutariTransportPolicy_notAttestableRefused — the D2 gate:
// enforcing a cnsa-2.0 floor on the hybrid-attesting CI deployment
// must fail the apply with the gateway's self-explaining 422, and
// fail it LOUDLY (no half-written state).
func TestAccScrutariTransportPolicy_notAttestableRefused(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccTransportPolicyConfig("cnsa-2.0", "enforce"),
				ExpectError: regexp.MustCompile(`(?i)transport_policy_not_attestable|cannot attest`),
			},
		},
	})
}

// TestAccScrutariTransportPolicy_monitorAcceptsUnattestable — the
// other half of the D2 contract: MONITORING an unattestable floor
// is accepted (honestly-unknowable verdicts are evidence), with
// attestable_here surfacing false so the operator can see why the
// floor is not yet enforceable here.
func TestAccScrutariTransportPolicy_monitorAcceptsUnattestable(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTransportPolicyReset,
		Steps: []resource.TestStep{
			{
				Config: testAccTransportPolicyConfig("cnsa-2.0", "monitor"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "min_posture", "cnsa-2.0"),
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "mode", "monitor"),
					resource.TestCheckResourceAttr("scrutari_transport_policy.test", "attestable_here", "false"),
				),
			},
		},
	})
}

// ─────────────────────────────────────────────────────────────────
// CheckDestroy — destroy means RESET, not 404 (D7).
//
// The document always exists; after destroy the gateway must read
// back the default (any/monitor). A 404 here would mean the
// singleton contract broke; a lingering floor would mean destroy
// leaked a traffic-affecting control — the worst kind of leak.
// ─────────────────────────────────────────────────────────────────

func testAccCheckTransportPolicyReset(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "scrutari_transport_policy" {
			continue
		}

		status, body, err := testAccAPIGet("/v1/transport_policy")
		if err != nil {
			return fmt.Errorf("verify-reset GET failed: %w", err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("transport policy GET returned HTTP %d after destroy (want 200 with the default document): %s", status, body)
		}

		var doc struct {
			MinPosture string `json:"min_posture"`
			Mode       string `json:"mode"`
		}
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			return fmt.Errorf("verify-reset parse: %w (body=%s)", err, body)
		}
		if doc.MinPosture != "any" || doc.Mode != "monitor" {
			return fmt.Errorf(
				"transport policy did not reset on destroy: min_posture=%q mode=%q "+
					"(want any/monitor) — destroy leaked a traffic-affecting control",
				doc.MinPosture, doc.Mode,
			)
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────
// HCL fixtures
// ─────────────────────────────────────────────────────────────────

func testAccTransportPolicyConfig(minPosture, mode string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_transport_policy" "test" {
  min_posture = %[1]q
  mode        = %[2]q
}
`, minPosture, mode)
}
