package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	// Generated wire DTOs — see `client.go` route comment block for
	// the hybrid-architecture rationale.
	gwclient "github.com/scrutari-ai/terraform-provider-scrutari/internal/client"
)

var (
	_ resource.Resource                = &APIKeyResource{}
	_ resource.ResourceWithConfigure   = &APIKeyResource{}
	_ resource.ResourceWithImportState = &APIKeyResource{}
)

func NewAPIKeyResource() resource.Resource {
	return &APIKeyResource{}
}

type APIKeyResource struct {
	client *Client
}

// APIKeyResourceModel mirrors the on-the-wire shape from
// `POST /v1/api_keys` and the masked list endpoint.
//
// Notable: `plaintext` is Computed + Sensitive, populated only by the
// Create response, and preserved across reads via UseStateForUnknown
// (the masked list endpoint never returns it again).
type APIKeyResourceModel struct {
	ID          types.String `tfsdk:"id"`
	TenantID    types.String `tfsdk:"tenant_id"`
	Name        types.String `tfsdk:"name"`
	Scopes      types.List   `tfsdk:"scopes"`
	Environment types.String `tfsdk:"environment"`
	KeyHint     types.String `tfsdk:"key_hint"`
	Plaintext   types.String `tfsdk:"plaintext"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (r *APIKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *APIKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A scoped API key for the Scrutari REST API. **The plaintext is returned once at creation and never again** — store it in a secret manager immediately. API keys are immutable; changing `name`, `scopes`, or `environment` forces replacement (revoke + reissue).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "API key id (`ak_<26-char-ulid>`). Stable across the key's lifetime.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant ID that owns this key, derived from the caller's authenticated key.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Human-readable name for the key (e.g. `terraform-ci`). 1-100 characters.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 100),
				},
			},
			"scopes": schema.ListAttribute{
				MarkdownDescription: "List of scopes granted to this key (e.g. `[\"routes:read\", \"routes:write\"]`). Use `[\"*\"]` for wildcard. See the API docs for the full vocabulary.",
				Required:            true,
				ElementType:         types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
					listvalidator.ValueStringsAre(
						stringvalidator.OneOf(
							"*",
							"api_keys:read", "api_keys:write",
							"routes:read", "routes:write",
							"domains:read", "domains:write",
							"members:read", "members:write",
							"tenants:read", "tenants:write",
							"audit_log:read",
						),
					),
				},
			},
			"environment": schema.StringAttribute{
				MarkdownDescription: "`live` (default) or `test`. Test keys hit a sandbox with no real billing.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("live"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("live", "test"),
				},
			},
			"key_hint": schema.StringAttribute{
				MarkdownDescription: "Last 4 characters of the key body, safe to display in UIs and logs.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"plaintext": schema.StringAttribute{
				MarkdownDescription: "Full plaintext key (`scru_(live|test)_<26-char-body>`). **Surfaced once at creation only.** Stored in Terraform state — use a secure state backend. The provider cannot recover this value if state is lost; revoke + reissue is the only path.",
				Computed:            true,
				Sensitive:           true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp when this key was issued.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *APIKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
	r.client = c
}

func (r *APIKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan APIKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var scopes []string
	resp.Diagnostics.Append(plan.Scopes.ElementsAs(ctx, &scopes, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// `CreateApiKeyRequest.Environment` is `*string` post-codegen
	// because the Rust side has `Option<String>` on the field
	// (gateway-side default applies when omitted). The Terraform
	// schema marks `environment` as Optional with a hard-coded
	// "live" default at the schema layer, so by the time we reach
	// this code path it's always a known value — just take the
	// address.
	environment := plan.Environment.ValueString()
	apiReq := gwclient.CreateApiKeyRequest{
		Name:        plan.Name.ValueString(),
		Scopes:      scopes,
		Environment: &environment,
	}

	idemKey := r.client.IdempotencyKey("scrutari_api_key."+plan.Name.ValueString(), "create")
	row, err := r.client.CreateAPIKey(ctx, apiReq, idemKey)
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to create API key", err)
		return
	}

	// Inline hydration — `plaintext` is captured here exactly once.
	// Subsequent Read calls hit the masked list endpoint which never
	// returns it; the schema's UseStateForUnknown plan modifier keeps
	// this value in state across plans. Field names follow oapi-codegen
	// CamelCase (Id / TenantId / KeyHint), not Go's preferred initialism
	// convention — same trade-off documented on route_resource.go's
	// hydrateModelFromAPI.
	plan.ID = types.StringValue(row.Id)
	plan.TenantID = types.StringValue(row.TenantId)
	plan.Name = types.StringValue(row.Name)
	scopesList, d := types.ListValueFrom(ctx, types.StringType, row.Scopes)
	resp.Diagnostics.Append(d...)
	plan.Scopes = scopesList
	plan.Environment = types.StringValue(row.Environment)
	plan.KeyHint = types.StringValue(row.KeyHint)
	plan.Plaintext = types.StringValue(row.Plaintext)
	plan.CreatedAt = types.StringValue(row.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *APIKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state APIKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Workaround for the missing GET /v1/api_keys/{id}: paginate the
	// masked list. Phase 6 followup adds the singular endpoint and we
	// drop this loop. Tenant-scoped key cardinality is small enough
	// that O(n) is fine in the meantime.
	masked, err := r.client.FindAPIKeyByID(ctx, state.ID.ValueString())
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to refresh API key state", err)
		return
	}
	if masked == nil {
		// Key revoked outside Terraform — drop from state so the next
		// apply re-creates if the config still references it.
		resp.State.RemoveResource(ctx)
		return
	}

	// Refresh server-side fields. plaintext is intentionally NOT
	// touched: the masked list doesn't return it, and overwriting
	// would either nuke state.Plaintext (bad — operator loses access)
	// or make the value perpetually "(known after apply)".
	//
	// `masked` is `*gwclient.ApiKeyResponse` — the no-plaintext shape.
	// `LastUsedAt` is `*string` (nullable on the wire) but the
	// Terraform model doesn't currently surface it; if a future
	// `last_used_at` schema attribute is added, dereference safely
	// with `if masked.LastUsedAt != nil`.
	state.TenantID = types.StringValue(masked.TenantId)
	state.Name = types.StringValue(masked.Name)
	scopesList, d := types.ListValueFrom(ctx, types.StringType, masked.Scopes)
	resp.Diagnostics.Append(d...)
	state.Scopes = scopesList
	state.Environment = types.StringValue(masked.Environment)
	state.KeyHint = types.StringValue(masked.KeyHint)
	state.CreatedAt = types.StringValue(masked.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is intentionally an error path. Every editable attribute on
// this resource has RequiresReplace, so the Terraform core never
// schedules an Update — Plan turns any change into Destroy + Create.
// The method exists only to satisfy the resource.Resource interface;
// reaching it means the schema's plan modifiers were bypassed (which
// would be a Plugin Framework bug, not a user error).
func (r *APIKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"API keys are immutable",
		"Every attribute on `scrutari_api_key` has `RequiresReplace`. If Terraform "+
			"reached this code path, the schema's plan modifiers were bypassed — please file a bug.",
	)
}

func (r *APIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state APIKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idemKey := r.client.IdempotencyKey("scrutari_api_key.id."+state.ID.ValueString(), "delete")
	if err := r.client.RevokeAPIKey(ctx, state.ID.ValueString(), idemKey); err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) && apiErr.Type == "not_found_error" {
			// Already revoked — terminal state matches desired state.
			return
		}
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to revoke API key", err)
	}
}

func (r *APIKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !strings.HasPrefix(req.ID, "ak_") {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected an `ak_<...>` API key id, got %q.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	// plaintext is unrecoverable on import — set explicitly to null.
	// Document this in the import example: imported keys lose
	// plaintext access forever; the operator must already have it
	// stashed in their secret manager from the original creation.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("plaintext"), types.StringNull())...)
}
