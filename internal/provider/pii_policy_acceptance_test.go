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
// scrutari_pii_policy acceptance tests (Track 3)
//
// Environment contract: TF_ACC=1, SCRUTARI_API_KEY=scru_test_* with
// pii_policy:read + pii_policy:write, SCRUTARI_HOST.
//
// SINGLETON: one document per tenant, so these tests serialize
// against each other and against any other suite that touches the CI
// tenant's PII policy. The framework runs tests in one process
// sequentially by default; do NOT add t.Parallel() here.
//
// REPLACE semantics shape every assertion: a `rule` block that is
// removed from config must revert that category to the `audit`
// default, not linger. The state never carries `audit` rows (audit is
// the absence of a rule), so the resource's `rule` set holds only the
// declared redact/block categories — that is what the checks below
// count.
// ─────────────────────────────────────────────────────────────────

// TestAccScrutariPiiPolicy_lifecycle — set a mixed policy, then update
// it (add, drop, and re-mode categories in place) and confirm the
// resource is updated rather than replaced.
func TestAccScrutariPiiPolicy_lifecycle(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPiiPolicyReset,
		Steps: []resource.TestStep{
			{
				// Two blocks, two redacts: the baseline mixed policy.
				Config: testAccPiiPolicyConfig(`
  rule {
    category = "ssn"
    mode     = "block"
  }
  rule {
    category = "credit_card"
    mode     = "block"
  }
  rule {
    category = "mrn"
    mode     = "redact"
  }
  rule {
    category = "icd10"
    mode     = "redact"
  }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_pii_policy.test", "id", "tenant"),
					resource.TestCheckResourceAttr("scrutari_pii_policy.test", "rule.#", "4"),
					resource.TestCheckResourceAttrSet("scrutari_pii_policy.test", "updated_at"),
					// The set is unordered; assert membership by value.
					resource.TestCheckTypeSetElemNestedAttrs("scrutari_pii_policy.test", "rule.*", map[string]string{
						"category": "ssn",
						"mode":     "block",
					}),
					resource.TestCheckTypeSetElemNestedAttrs("scrutari_pii_policy.test", "rule.*", map[string]string{
						"category": "mrn",
						"mode":     "redact",
					}),
				),
			},
			{
				// In-place update: drop credit_card and icd10 (back to
				// audit), promote mrn redact -> block, and add email.
				// The count moves 4 -> 3 and the resource must NOT be
				// replaced.
				Config: testAccPiiPolicyConfig(`
  rule {
    category = "ssn"
    mode     = "block"
  }
  rule {
    category = "mrn"
    mode     = "block"
  }
  rule {
    category = "email"
    mode     = "redact"
  }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_pii_policy.test", "rule.#", "3"),
					resource.TestCheckTypeSetElemNestedAttrs("scrutari_pii_policy.test", "rule.*", map[string]string{
						"category": "mrn",
						"mode":     "block",
					}),
					resource.TestCheckTypeSetElemNestedAttrs("scrutari_pii_policy.test", "rule.*", map[string]string{
						"category": "email",
						"mode":     "redact",
					}),
				),
			},
			{
				// Empty policy: every category reverts to audit, so the
				// set is empty. This is the "stop enforcing without
				// destroying the resource" path.
				Config: testAccPiiPolicyConfig(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("scrutari_pii_policy.test", "rule.#", "0"),
				),
			},
		},
	})
}

// TestAccScrutariPiiPolicy_import — singleton import via the literal
// `tenant` id. Every attribute is readable, so the imported state must
// match exactly (no VerifyIgnore).
func TestAccScrutariPiiPolicy_import(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPiiPolicyReset,
		Steps: []resource.TestStep{
			{
				Config: testAccPiiPolicyConfig(`
  rule {
    category = "ssn"
    mode     = "block"
  }
  rule {
    category = "email"
    mode     = "redact"
  }`),
			},
			{
				ResourceName:      "scrutari_pii_policy.test",
				ImportState:       true,
				ImportStateId:     "tenant",
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccScrutariPiiPolicy_invalidModeRejected — `mode = "audit"` is
// not a writable value at this surface (audit is the absence of a
// rule), so the schema validator must reject it at plan time, before
// any call reaches the gateway.
func TestAccScrutariPiiPolicy_invalidModeRejected(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPiiPolicyConfig(`
  rule {
    category = "ssn"
    mode     = "audit"
  }`),
				ExpectError: regexp.MustCompile(`(?i)Attribute rule\[\S+\]\.mode value must be one of|"redact"`),
			},
		},
	})
}

// TestAccScrutariPiiPolicy_invalidCategoryRejected — an unknown
// category is a typo, and a typo on a security control must fail loud
// at plan time, not silently no-op.
func TestAccScrutariPiiPolicy_invalidCategoryRejected(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPiiPolicyConfig(`
  rule {
    category = "passport"
    mode     = "block"
  }`),
				ExpectError: regexp.MustCompile(`(?i)Attribute rule\[\S+\]\.category value must be one of|"ssn"`),
			},
		},
	})
}

// ─────────────────────────────────────────────────────────────────
// CheckDestroy — destroy means RESET, not 404.
//
// The document always exists; after destroy the gateway must read
// back the all-audit default. A 404 here would mean the singleton
// contract broke; any lingering redact/block category would mean
// destroy leaked an active data-loss-prevention control.
// ─────────────────────────────────────────────────────────────────

func testAccCheckPiiPolicyReset(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "scrutari_pii_policy" {
			continue
		}

		status, body, err := testAccAPIGet("/v1/pii_policy")
		if err != nil {
			return fmt.Errorf("verify-reset GET failed: %w", err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("pii policy GET returned HTTP %d after destroy (want 200 with the default document): %s", status, body)
		}

		var doc struct {
			Policies []struct {
				Category string `json:"category"`
				Mode     string `json:"mode"`
			} `json:"policies"`
		}
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			return fmt.Errorf("verify-reset parse: %w (body=%s)", err, body)
		}
		for _, p := range doc.Policies {
			if p.Mode != "audit" {
				return fmt.Errorf(
					"pii policy did not reset on destroy: category %q is %q (want audit) "+
						"— destroy leaked an active data-loss-prevention control",
					p.Category, p.Mode,
				)
			}
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────
// HCL fixtures
// ─────────────────────────────────────────────────────────────────

// testAccPiiPolicyConfig wraps a set of `rule` blocks (or the empty
// string for the all-audit policy) in the provider + resource shell.
func testAccPiiPolicyConfig(rules string) string {
	return fmt.Sprintf(`
provider "scrutari" {}

resource "scrutari_pii_policy" "test" {%s
}
`, rules)
}
