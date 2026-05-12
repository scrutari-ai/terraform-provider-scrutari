package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	// Generated wire DTOs — see the comment block on the route
	// endpoint methods in `client.go` for the rationale on importing
	// these as `gwclient` and keeping the hand-rolled HTTP client.
	gwclient "github.com/scrutari-ai/terraform-provider-scrutari/internal/client"
)

var (
	_ resource.Resource                = &RouteResource{}
	_ resource.ResourceWithConfigure   = &RouteResource{}
	_ resource.ResourceWithImportState = &RouteResource{}
)

func NewRouteResource() resource.Resource {
	return &RouteResource{}
}

type RouteResource struct {
	client *Client
}

// RouteResourceModel maps each .tf attribute to a tfsdk-tagged field.
// types.* values carry "unknown / null / value" tri-state — the Plugin
// Framework relies on that to track plan-time vs apply-time values
// vs the absence of a value, so we don't reach for plain Go types here.
type RouteResourceModel struct {
	ID                 types.Int64  `tfsdk:"id"`
	TenantID           types.String `tfsdk:"tenant_id"`
	Host               types.String `tfsdk:"host"`
	PathPrefix         types.String `tfsdk:"path_prefix"`
	UpstreamURL        types.String `tfsdk:"upstream_url"`
	StripPathPrefix    types.Bool   `tfsdk:"strip_path_prefix"`
	PreserveHostHeader types.Bool   `tfsdk:"preserve_host_header"`
	IsActive           types.Bool   `tfsdk:"is_active"`
	AuthMode           types.String `tfsdk:"auth_mode"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
}

func (r *RouteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_route"
}

func (r *RouteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Routing rule that maps `(host, path_prefix)` to an upstream service. Tenant scope is derived from the authenticated API key.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Route ID assigned by the gateway. Stable across the row's lifetime.",
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant ID that owns this route, derived from the authenticated API key. Read-only.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"host": schema.StringAttribute{
				MarkdownDescription: "Public host the gateway listens on for this rule (e.g. `api.acme-corp.scrutari.ai`). Lower-cased server-side. Cannot be changed in place — re-homing a route is delete-then-create.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"path_prefix": schema.StringAttribute{
				MarkdownDescription: "Path prefix this rule matches (e.g. `/billing`). Use `/` as the catch-all.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^/`), "must start with '/'"),
				},
			},
			"upstream_url": schema.StringAttribute{
				MarkdownDescription: "Upstream URL the gateway forwards matching traffic to. Must be `http(s)://...`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^https?://[^\s]+$`), "must be http(s)://... with a host and no spaces"),
				},
			},
			"strip_path_prefix": schema.BoolAttribute{
				MarkdownDescription: "If true, the matched `path_prefix` is stripped from the request path before forwarding to the upstream. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"preserve_host_header": schema.BoolAttribute{
				MarkdownDescription: "If true, the original `Host` header is forwarded to the upstream; otherwise the upstream's host is used. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"auth_mode": schema.StringAttribute{
				MarkdownDescription: "Authentication mode for traffic on this route: `required` (default), `optional`, or `passthrough`. **Setting to `passthrough` opens the route to anonymous traffic** — the gateway's audit log records this at WARN severity.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("required"),
				Validators: []validator.String{
					stringvalidator.OneOf("required", "optional", "passthrough"),
				},
			},
			"is_active": schema.BoolAttribute{
				MarkdownDescription: "Whether this route is active. Inactive routes don't match traffic but still occupy a plan-cap slot. Defaults to `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp when this route was created.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of the most recent update.",
				Computed:            true,
			},
		},
	}
}

func (r *RouteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *Client, got %T. Please report this issue.", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *RouteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RouteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// `CreateRouteRequest` from the generated package treats the three
	// `#[serde(default)]` Rust fields (strip_path_prefix,
	// preserve_host_header, auth_mode) as Go-side `*bool` / `*string`
	// pointers — they're optional on the wire so the gateway-side
	// `unwrap_or_default` path applies when omitted. The Terraform
	// schema marks them Optional with explicit defaults at the
	// resource-schema layer (`booldefault.StaticBool(false)`,
	// `stringdefault.StaticString("required")`), so by the time we
	// reach this code path the values are always known — we just
	// have to take the address. Stash them as locals so `&v` is
	// valid; you can't take the address of a function-call return
	// value in Go.
	stripPathPrefix := plan.StripPathPrefix.ValueBool()
	preserveHostHeader := plan.PreserveHostHeader.ValueBool()
	authMode := plan.AuthMode.ValueString()
	apiReq := gwclient.CreateRouteRequest{
		Host:               plan.Host.ValueString(),
		PathPrefix:         plan.PathPrefix.ValueString(),
		UpstreamUrl:        plan.UpstreamURL.ValueString(),
		StripPathPrefix:    &stripPathPrefix,
		PreserveHostHeader: &preserveHostHeader,
		AuthMode:           &authMode,
	}

	idemKey := r.client.IdempotencyKey("scrutari_route."+plan.Host.ValueString()+plan.PathPrefix.ValueString(), "create")
	row, err := r.client.CreateRoute(ctx, apiReq, idemKey)
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to create route", err)
		return
	}

	// is_active is gateway-controlled at create time (defaults to TRUE,
	// no field to override on POST). If the plan asked for is_active=false,
	// we PATCH immediately to satisfy the request. Belt-and-suspenders
	// for the case where the gateway later changes its create-time
	// default.
	//
	// `row.Id` is `int32` post-codegen (matches the Rust-side `i32`);
	// widening to `int64` for the client call is infallible.
	if !plan.IsActive.IsNull() && !plan.IsActive.IsUnknown() && plan.IsActive.ValueBool() != row.IsActive {
		desiredIsActive := plan.IsActive.ValueBool()
		row, err = r.client.UpdateRoute(ctx, int64(row.Id), gwclient.UpdateRouteRequest{IsActive: &desiredIsActive},
			r.client.IdempotencyKey("scrutari_route."+plan.Host.ValueString()+plan.PathPrefix.ValueString(), "create-then-deactivate"))
		if err != nil {
			appendAPIErrorDiag(&resp.Diagnostics, "Failed to set is_active after create", err)
			return
		}
	}

	hydrateModelFromAPI(&plan, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *RouteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RouteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	row, err := r.client.GetRoute(ctx, state.ID.ValueInt64())
	if err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) && apiErr.Type == "not_found_error" {
			// Row vanished server-side. Drop from state so Terraform
			// re-creates on the next apply (matches Terraform's
			// expected drift-recovery semantics).
			resp.State.RemoveResource(ctx)
			return
		}
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to read route", err)
		return
	}

	hydrateModelFromAPI(&state, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *RouteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RouteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	patch := buildRoutePatch(&plan, &state)
	idemKey := r.client.IdempotencyKey(fmt.Sprintf("scrutari_route.id.%d", state.ID.ValueInt64()), "update")
	row, err := r.client.UpdateRoute(ctx, state.ID.ValueInt64(), patch, idemKey)
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to update route", err)
		return
	}

	hydrateModelFromAPI(&plan, row)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *RouteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RouteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idemKey := r.client.IdempotencyKey(fmt.Sprintf("scrutari_route.id.%d", state.ID.ValueInt64()), "delete")
	if err := r.client.DeleteRoute(ctx, state.ID.ValueInt64(), idemKey); err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) && apiErr.Type == "not_found_error" {
			// Already gone — treat as success.
			return
		}
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to delete route", err)
		return
	}
}

func (r *RouteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected a numeric route id (e.g. `terraform import scrutari_route.example 42`), got %q: %s", req.ID, err),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ─── Helpers ───────────────────────────────────────────────────────

// hydrateModelFromAPI overwrites every Computed and round-trip field
// in the model from the API response, leaving Required/Optional fields
// the user supplied untouched (they're already in the model from the
// plan or state). This is what makes drift detection work: the next
// Read() refreshes server-side fields, and Terraform compares against
// the prior plan to surface diffs.
func hydrateModelFromAPI(m *RouteResourceModel, row *gwclient.RouteResponse) {
	// oapi-codegen names: `Id` / `TenantId` / `UpstreamUrl` (basic
	// CamelCase, not Go's preferred initialism convention). We cast
	// `row.Id` (int32, mirroring the gateway's `i32`) to int64 to fit
	// the Terraform schema's `types.Int64Value` — the widening is
	// infallible and keeps the Terraform-facing schema stable for the
	// existing v0.2 state files.
	m.ID = types.Int64Value(int64(row.Id))
	m.TenantID = types.StringValue(row.TenantId)
	m.Host = types.StringValue(row.Host)
	m.PathPrefix = types.StringValue(row.PathPrefix)
	m.UpstreamURL = types.StringValue(row.UpstreamUrl)
	m.StripPathPrefix = types.BoolValue(row.StripPathPrefix)
	m.PreserveHostHeader = types.BoolValue(row.PreserveHostHeader)
	m.IsActive = types.BoolValue(row.IsActive)
	m.AuthMode = types.StringValue(row.AuthMode)
	m.CreatedAt = types.StringValue(row.CreatedAt)
	m.UpdatedAt = types.StringValue(row.UpdatedAt)
}

// buildRoutePatch produces a PATCH body containing only fields that
// differ between plan and state. This minimizes the payload AND
// matches the gateway's `route.updated` event semantics — the
// dashboard's audit log will show a tight diff rather than every
// field repeated.
//
// Note: host is intentionally NOT in the patch. RequiresReplace at
// the schema level means a host change goes through Delete+Create,
// not Update.
// `gwclient.UpdateRouteRequest` matches the gateway's PATCH-body
// shape from `libs/api_types::UpdateRouteRequest`: every field is
// `Option<T>` on the Rust side and lands as `*T` on the Go side
// after codegen, so "omitted = unchanged" maps cleanly onto a nil
// pointer. We only set pointers for fields whose plan value differs
// from state — the gateway's `COALESCE($n, column)` SQL pattern
// then leaves untouched columns alone. Net wire payload is minimal,
// and the dashboard's audit log shows a tight diff (one event per
// real change) rather than a noisy "every field" event per PATCH.
//
// Note: `UpstreamURL` on the Terraform model maps to `UpstreamUrl`
// on the generated type (oapi-codegen's basic CamelCase) — same
// renaming pattern as `hydrateModelFromAPI`.
func buildRoutePatch(plan, state *RouteResourceModel) gwclient.UpdateRouteRequest {
	var p gwclient.UpdateRouteRequest
	if !plan.PathPrefix.Equal(state.PathPrefix) {
		v := plan.PathPrefix.ValueString()
		p.PathPrefix = &v
	}
	if !plan.UpstreamURL.Equal(state.UpstreamURL) {
		v := plan.UpstreamURL.ValueString()
		p.UpstreamUrl = &v
	}
	if !plan.StripPathPrefix.Equal(state.StripPathPrefix) {
		v := plan.StripPathPrefix.ValueBool()
		p.StripPathPrefix = &v
	}
	if !plan.PreserveHostHeader.Equal(state.PreserveHostHeader) {
		v := plan.PreserveHostHeader.ValueBool()
		p.PreserveHostHeader = &v
	}
	if !plan.AuthMode.Equal(state.AuthMode) {
		v := plan.AuthMode.ValueString()
		p.AuthMode = &v
	}
	if !plan.IsActive.Equal(state.IsActive) {
		v := plan.IsActive.ValueBool()
		p.IsActive = &v
	}
	return p
}

// appendAPIErrorDiag turns a Scrutari APIError into a Terraform
// diagnostic with the request_id surfaced for support tickets.
// Special-cases the upgrade-nudge code so customers see a clear
// hint instead of a generic 400.
func appendAPIErrorDiag(diags interface {
	AddError(string, string)
	AddWarning(string, string)
}, summary string, err error) {
	var apiErr *APIError
	if asAPIError(err, &apiErr) {
		switch apiErr.Code {
		case "plan_limit_reached":
			diags.AddError(
				"Plan limit reached",
				fmt.Sprintf("%s\n\n%s\n\nUpgrade your plan via the dashboard's Billing page, or remove unused resources to free a slot. (request_id: %s)",
					summary, apiErr.Message, apiErr.RequestID),
			)
		case "domain_not_verified":
			diags.AddError(
				"Domain not verified",
				fmt.Sprintf("%s\n\n%s\n\nVerify the domain via the dashboard's Domains page, then retry. (request_id: %s)",
					summary, apiErr.Message, apiErr.RequestID),
			)
		default:
			diags.AddError(summary, fmt.Sprintf("%s (request_id: %s)", apiErr.Error(), apiErr.RequestID))
		}
		return
	}
	diags.AddError(summary, err.Error())
}
