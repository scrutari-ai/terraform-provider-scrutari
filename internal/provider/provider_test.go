package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// ─────────────────────────────────────────────────────────────────
// Provider factory for the protocol-6 plugin harness.
//
// Each `resource.Test(t, ...)` call instantiates a fresh provider
// server through this factory, so any in-provider state (Client
// pointer, idempotency-key state, etc.) is reset between cases.
// That isolation is what lets us run tests in any order without
// hidden cross-test contamination.
// ─────────────────────────────────────────────────────────────────

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"scrutari": providerserver.NewProtocol6WithError(New("acceptance")()),
}

// ─────────────────────────────────────────────────────────────────
// PreCheck — the single most important safety belt in this suite.
//
// SCRUTARI_API_KEY + SCRUTARI_HOST must both be set; the key MUST
// have the scru_test_* prefix. A live key here would create real
// routes on a paying tenant the moment the test boots. We refuse
// to start in that configuration — fail loud, fail early.
// ─────────────────────────────────────────────────────────────────

func testAccPreCheck(t *testing.T) {
	t.Helper()

	for _, name := range []string{"SCRUTARI_API_KEY", "SCRUTARI_HOST"} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s must be set to run acceptance tests", name)
		}
	}

	key := os.Getenv("SCRUTARI_API_KEY")
	if !strings.HasPrefix(key, "scru_test_") {
		// Show only the prefix in the failure message — never the
		// full secret, even on a t.Fatalf path.
		t.Fatalf(
			"refusing to run acceptance tests with a non-test key "+
				"(must start with scru_test_*; got prefix %q). "+
				"Set SCRUTARI_API_KEY to the SCRUTARI_CI_KEY from "+
				"the tnt_terraform_provider_ci sandbox tenant.",
			safePrefix(key, 11),
		)
	}
}

// safePrefix returns at most n bytes of s. Used in error messages
// to ensure we never echo a full secret to stderr.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ─────────────────────────────────────────────────────────────────
// Pre-seeded fixture data on the CI sandbox tenant.
//
// The three hosts below were inserted as `status='verified'` by
// scripts/provision_ci_tenant.sh. Tests rotate through them so a
// previously-orphaned route on host A doesn't block a re-run on
// host B. RFC 2606 reserves `.test`, so these names will never
// collide with real DNS.
// ─────────────────────────────────────────────────────────────────

var testAccPreVerifiedHosts = []string{
	"terraform-ci-a.test",
	"terraform-ci-b.test",
	"terraform-ci-c.test",
}

// uniquePathPrefix returns a path prefix unique to this test
// invocation. Combining the test name with a nanosecond timestamp
// guarantees no collision even if a previous run was interrupted
// before CheckDestroy cleared the route — a stale `/orders` from
// yesterday won't cause today's basic test to 409.
func uniquePathPrefix(t *testing.T) string {
	t.Helper()
	clean := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return fmt.Sprintf("/acc-%s-%d", clean, time.Now().UnixNano())
}

// ─────────────────────────────────────────────────────────────────
// Out-of-band API client.
//
// CheckDestroy needs to ask the gateway "is this resource really
// gone?" — the framework's TestCheckResourceAttr only inspects
// Terraform state, which can drift from reality. We do that with a
// minimal direct HTTP GET rather than reusing the provider's
// internal Client (which would require exporting it from the
// provider package or threading it through test setup).
// ─────────────────────────────────────────────────────────────────

// testAccAPIGet returns (statusCode, body, transportError).
// Non-2xx is NOT a transport error — 404 is the expected response
// for a CheckDestroy verification call.
func testAccAPIGet(path string) (int, string, error) {
	host := os.Getenv("SCRUTARI_HOST")
	key := os.Getenv("SCRUTARI_API_KEY")
	url := strings.TrimRight(host, "/") + path

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "terraform-provider-scrutari-acceptance/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, string(body), nil
}
