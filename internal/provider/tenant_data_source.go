package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	// Generated wire DTOs — see `client.go` route comment block for
	// the hybrid-architecture rationale.
	gwclient "github.com/scrutari-ai/terraform-provider-scrutari/internal/client"
)

var (
	_ datasource.DataSource              = &TenantDataSource{}
	_ datasource.DataSourceWithConfigure = &TenantDataSource{}
)

func NewTenantDataSource() datasource.DataSource {
	return &TenantDataSource{}
}

type TenantDataSource struct {
	client *Client
}

type TenantDataSourceModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Plan           types.String `tfsdk:"plan"`
	RateLimitRPS   types.Int64  `tfsdk:"rate_limit_rps"`
	RateLimitBurst types.Int64  `tfsdk:"rate_limit_burst"`
	ConcurrencyCap types.Int64  `tfsdk:"concurrency_cap"`
	Features       types.String `tfsdk:"features"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

// Local wire DTO removed; the gateway's `TenantResponse` is now
// imported from `gwclient`. Worth flagging on the contract surface:
// the gateway-side `Features` field is `serde_json::Value` (a
// free-form JSON object), which oapi-codegen renders as
// `map[string]interface{}`. We re-encode it to a JSON string in
// the Terraform model so HCL users can `jsondecode()` to read
// individual flags.

func (d *TenantDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant"
}

func (d *TenantDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The tenant owned by the authenticated API key. There is no `id` lookup parameter — the gateway's tenant-isolation discipline (Task #54) means each key is scoped to exactly one tenant, so the data source returns that tenant unconditionally.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Tenant ID slug (`tnt_<26-char-ulid>`). Stable across the tenant's lifetime.",
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Customer-facing display name for the tenant. Set during onboarding; editable via the dashboard.",
			},
			"plan": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Subscription tier — one of `starter` / `growth` / `enterprise`. Drives rate-limit and concurrency caps below.",
			},
			"rate_limit_rps": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Steady-state request-rate cap (requests/second) the gateway enforces for this tenant. Set by the plan tier.",
			},
			"rate_limit_burst": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Burst capacity above `rate_limit_rps` — the token-bucket depth the limiter allows before throttling kicks in.",
			},
			"concurrency_cap": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Maximum number of in-flight requests the gateway accepts per tenant before shedding load with `503 Service Unavailable`.",
			},
			"features": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "JSON-encoded feature flag bag. Use `jsondecode()` in HCL to read individual flags. Unknown keys are treated as absent (false).",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "RFC 3339 timestamp when this tenant was provisioned.",
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "RFC 3339 timestamp of the most recent change to the tenant row (plan tier, name, feature flags, etc.).",
			},
		},
	}
}

func (d *TenantDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *Client, got %T.", req.ProviderData),
		)
		return
	}
	d.client = c
}

func (d *TenantDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// /v1/tenants returns a one-row Page<TenantResponse>. We extract
	// data[0]; if data is empty, the tenant was soft-deleted between
	// the key's issuance and this call — surface a clear diagnostic
	// rather than a confusing nil-pointer panic.
	//
	// `gwclient.PageTenantResponse` is the generated paginated
	// envelope; `Data` is `[]gwclient.TenantResponse`.
	var page gwclient.PageTenantResponse
	if err := d.client.do(ctx, "GET", "/v1/tenants", nil, &page, ""); err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to read tenant", err)
		return
	}
	if len(page.Data) == 0 {
		resp.Diagnostics.AddError(
			"Tenant not found",
			"The /v1/tenants endpoint returned an empty list. The tenant owning the authenticated API key may have been soft-deleted.",
		)
		return
	}

	// Field-name notes (oapi-codegen CamelCase): Id / RateLimitRps
	// (not RateLimitRPS) / ConcurrencyCap. Rate-limit and concurrency
	// fields are int32 on the wire (the Rust types use `i32` —
	// suitable for the published rate caps which fit comfortably in
	// signed 32-bit); we widen to int64 at the Terraform-model
	// boundary to match the schema's `types.Int64` attribute type.
	tenantRow := page.Data[0]
	model := TenantDataSourceModel{
		ID:             types.StringValue(tenantRow.Id),
		Name:           types.StringValue(tenantRow.Name),
		Plan:           types.StringValue(tenantRow.Plan),
		RateLimitRPS:   types.Int64Value(int64(tenantRow.RateLimitRps)),
		RateLimitBurst: types.Int64Value(int64(tenantRow.RateLimitBurst)),
		ConcurrencyCap: types.Int64Value(int64(tenantRow.ConcurrencyCap)),
		CreatedAt:      types.StringValue(tenantRow.CreatedAt),
		UpdatedAt:      types.StringValue(tenantRow.UpdatedAt),
	}
	// Features is `map[string]interface{}` post-codegen (the
	// gateway-side Rust `serde_json::Value` projects through the
	// OpenAPI `type: "object"` schema to that Go type). We
	// re-encode to a JSON string so HCL users can `jsondecode()`
	// individual flags. If marshal fails — it shouldn't, since the
	// value just round-tripped through json.Decode — fall back to
	// "{}" rather than crashing. `nil` is impossible per the
	// generated type (Features is in the `required` array on the
	// gateway-side TenantResponse schema), but the defence is cheap.
	if tenantRow.Features != nil {
		if buf, err := json.Marshal(tenantRow.Features); err == nil {
			model.Features = types.StringValue(string(buf))
		} else {
			model.Features = types.StringValue("{}")
		}
	} else {
		model.Features = types.StringValue("{}")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}
