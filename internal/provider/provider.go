package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &ScrutariProvider{}

type ScrutariProvider struct {
	// version is set at build time via -ldflags. Surfaced into the
	// HTTP User-Agent header so gateway-side audit logs can attribute
	// requests to a specific provider version when something goes wrong.
	version string
}

// ScrutariProviderModel describes the provider configuration block.
// HCL-side names match the tfsdk tags below.
type ScrutariProviderModel struct {
	Host   types.String `tfsdk:"host"`
	APIKey types.String `tfsdk:"api_key"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &ScrutariProvider{version: version}
	}
}

func (p *ScrutariProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "scrutari"
	resp.Version = p.version
}

func (p *ScrutariProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Scrutari provider manages routing, API keys, and other resources on a Scrutari post-quantum gateway via the public REST API.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				MarkdownDescription: "Base URL of the Scrutari gateway's control-plane API. May also be set via the `SCRUTARI_HOST` env var. Defaults to `https://api.edge.scrutari.ai` when unset.",
				Optional:            true,
			},
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Scoped API key minted via `POST /v1/api_keys` or the dashboard. Must start with `scru_live_` or `scru_test_`. May also be set via the `SCRUTARI_API_KEY` env var (recommended — keeps secrets out of `.tf` files).",
				Optional:            true,
				Sensitive:           true,
			},
		},
	}
}

func (p *ScrutariProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data ScrutariProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolution order (deliberately fail-loud, not silent fallback):
	//   1. Explicit provider config block.
	//   2. Env var.
	//   3. For host only: documented production default.
	//   4. For api_key: hard error (no default — secrets never default).
	host := data.Host.ValueString()
	if host == "" {
		host = os.Getenv("SCRUTARI_HOST")
	}
	if host == "" {
		host = "https://api.edge.scrutari.ai"
	}

	apiKey := data.APIKey.ValueString()
	if apiKey == "" {
		apiKey = os.Getenv("SCRUTARI_API_KEY")
	}
	if apiKey == "" {
		resp.Diagnostics.AddError(
			"Missing Scrutari API key",
			"The provider requires an API key. Set the `api_key` attribute on the `provider \"scrutari\"` block, "+
				"or export `SCRUTARI_API_KEY` in the environment. Mint a key via the dashboard's API Keys page or `POST /v1/api_keys`.",
		)
		return
	}

	client, err := NewClient(host, apiKey, p.version)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Scrutari provider configuration", err.Error())
		return
	}

	// Health check fails the whole `terraform plan` if the gateway
	// is unreachable or the key is rejected. Better than 30 obscure
	// errors on individual resources.
	if err := client.HealthCheck(ctx); err != nil {
		resp.Diagnostics.AddError(
			"Scrutari gateway is not reachable or the API key is rejected",
			err.Error(),
		)
		return
	}

	// Pass the client to every resource + data source.
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *ScrutariProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewRouteResource,
		NewAPIKeyResource,
		// RFC-010 S4: the enterprise control-plane pair. A zone is
		// delegated once (scrutari_delegated_zone), then hostnames
		// under it are pure Terraform (scrutari_domain with zone_id)
		// — the "delegate in the morning, apply 500 hostnames before
		// lunch" story.
		NewDomainResource,
		NewDelegatedZoneResource,
	}
}

func (p *ScrutariProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewTenantDataSource,
		NewDomainDataSource,
		NewMemberDataSource,
	}
}
