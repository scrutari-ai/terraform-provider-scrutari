package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/scrutari-ai/terraform-provider-scrutari/internal/provider"
)

// version is overwritten at release time via -ldflags
// (e.g. `go build -ldflags "-X main.version=0.1.0"`).
// Default "dev" makes local builds traceable in the User-Agent
// and in any audit log lines on the gateway side.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider in debugger mode (waits for Terraform to attach via TF_REATTACH_PROVIDERS)")
	flag.Parse()

	opts := providerserver.ServeOpts{
		// This MUST match the source = "scrutari/scrutari" line in
		// every consumer's terraform { required_providers {} } block,
		// AND the dev_overrides key in ~/.terraformrc.
		// Mismatch → Terraform silently ignores the dev binary and
		// downloads from the registry instead, producing very
		// confusing "did my changes apply?" debugging.
		Address: "registry.terraform.io/scrutari/scrutari",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
