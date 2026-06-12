package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	gwclient "github.com/scrutari-ai/terraform-provider-scrutari/internal/client"
)

var (
	_ resource.Resource                = &DomainResource{}
	_ resource.ResourceWithConfigure   = &DomainResource{}
	_ resource.ResourceWithImportState = &DomainResource{}
)

func NewDomainResource() resource.Resource {
	return &DomainResource{}
}

type DomainResource struct {
	client *Client
}

// DomainResourceModel — see the schema below for field semantics.
// Two create modes share this one model (RFC-010 D4: one domain
// population): classic TXT-challenge rows (zone_id null) and
// zone-backed rows born verified (zone_id set).
type DomainResourceModel struct {
	ID               types.Int64  `tfsdk:"id"`
	TenantID         types.String `tfsdk:"tenant_id"`
	Domain           types.String `tfsdk:"domain"`
	ZoneID           types.String `tfsdk:"zone_id"`
	VerificationType types.String `tfsdk:"verification_type"`
	ChallengeHost    types.String `tfsdk:"challenge_host"`
	ExpectedValue    types.String `tfsdk:"expected_value"`
	Status           types.String `tfsdk:"status"`
	CreatedAt        types.String `tfsdk:"created_at"`
}

func (r *DomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (r *DomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A domain verification owned by the tenant. Two modes:\n\n" +
			"  * **Classic** (no `zone_id`): the resource is created `pending` and the gateway returns a one-time " +
			"TXT challenge (`challenge_host` + `expected_value`) to publish in your DNS. Pair with a DNS provider " +
			"resource to publish the record from the same configuration; the gateway's verifier flips the row to " +
			"`verified` once the record resolves.\n" +
			"  * **Zone-backed** (`zone_id` set to an `scrutari_delegated_zone` id): the hostname must sit exactly one " +
			"label under an *active* delegated zone and is born `verified` with nothing to publish — traffic is " +
			"served by the zone's wildcard certificate.\n\n" +
			"Destroying this resource cascades server-side: every `scrutari_route` on the same hostname is deleted " +
			"in the same transaction.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Verification row ID assigned by the gateway. Import key: `terraform import scrutari_domain.example 17`.",
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Tenant that owns this domain, derived from the authenticated API key. Read-only.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"domain": schema.StringAttribute{
				MarkdownDescription: "The fully-qualified hostname (bare, no scheme/port). Lower-cased server-side. Changing it replaces the resource — a verification is proof about one specific name.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(fqdnRegex, "must be a bare fully-qualified hostname (no scheme, port, or path)"),
				},
			},
			"zone_id": schema.StringAttribute{
				MarkdownDescription: "Optional delegated zone (UUID from `scrutari_delegated_zone.<name>.id`) to create the hostname under. " +
					"The zone must be `active` and the hostname exactly one label under it (that is what the zone's `*.<zone>` " +
					"wildcard certificate covers; the apex and deeper names are refused). Changing it replaces the resource. " +
					"Not refreshed on read or populated by import — the read API does not expose it yet.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(uuidRegex, "must be a zone UUID"),
				},
			},
			"verification_type": schema.StringAttribute{
				MarkdownDescription: "Verification method recorded on the row (`txt` for API-created rows).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"challenge_host": schema.StringAttribute{
				MarkdownDescription: "DNS name the TXT record must be published at (classic mode). Informational for zone-backed rows.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"expected_value": schema.StringAttribute{
				MarkdownDescription: "The exact TXT record value to publish (classic mode). **Delivered exactly once, on create**: the read " +
					"API never returns it, so the value lives only in Terraform state (treat your state accordingly) and an " +
					"imported resource has it null. Null for zone-backed creates — there is nothing to publish.",
				Computed:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Verification status: `pending` / `verified` / `failed` / `expired`. Classic rows are born `pending` " +
					"and flip server-side once the TXT record resolves; zone-backed rows are born `verified`.",
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp when the verification row was created.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *DomainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *DomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DomainResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := gwclient.CreateDomainRequest{
		Domain: plan.Domain.ValueString(),
	}
	if !plan.ZoneID.IsNull() && !plan.ZoneID.IsUnknown() {
		zoneID := plan.ZoneID.ValueString()
		apiReq.ZoneId = &zoneID
	}

	idemKey := r.client.IdempotencyKey("scrutari_domain."+plan.Domain.ValueString(), "create")
	row, err := r.client.CreateDomain(ctx, apiReq, idemKey)
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to create domain", err)
		return
	}

	plan.ID = types.Int64Value(int64(row.Id))
	plan.TenantID = types.StringValue(row.TenantId)
	plan.Domain = types.StringValue(row.Domain)
	plan.VerificationType = types.StringValue(row.VerificationType)
	plan.ChallengeHost = types.StringValue(row.ChallengeHost)
	// Present exactly once, only here, only for classic creates.
	// `StringPointerValue` maps the zone-backed nil onto a Terraform
	// null, which is the honest shape: nothing to publish.
	plan.ExpectedValue = types.StringPointerValue(row.ExpectedValue)
	plan.Status = types.StringValue(row.Status)
	plan.CreatedAt = types.StringValue(row.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *DomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DomainResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	row, err := r.client.GetDomain(ctx, state.ID.ValueInt64())
	if err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) && apiErr.Type == "not_found_error" {
			resp.State.RemoveResource(ctx)
			return
		}
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to read domain", err)
		return
	}

	// Refresh the server-owned fields. Two are deliberately LEFT
	// ALONE: `expected_value` (create-only secret the read API never
	// returns — overwriting would erase it from state on the first
	// refresh) and `zone_id` (config-only; the read shape does not
	// expose `delegated_zone_id` yet, see the schema note).
	state.TenantID = types.StringValue(row.TenantId)
	state.Domain = types.StringValue(row.Domain)
	state.VerificationType = types.StringValue(row.VerificationType)
	state.ChallengeHost = types.StringValue(row.ChallengeHost)
	state.Status = types.StringValue(row.Status)
	state.CreatedAt = types.StringValue(row.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is structurally unreachable: every user-settable attribute
// (`domain`, `zone_id`) carries RequiresReplace and everything else
// is Computed, so the framework routes all changes through
// Delete+Create. The method exists to satisfy the Resource interface;
// reaching it means a schema edit broke that invariant — fail loud.
func (r *DomainResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"In-place update is not supported for scrutari_domain",
		"All mutable attributes require replacement; this code path should be unreachable. Please report this issue.",
	)
}

func (r *DomainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DomainResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idemKey := r.client.IdempotencyKey(fmt.Sprintf("scrutari_domain.id.%d", state.ID.ValueInt64()), "delete")
	if err := r.client.DeleteDomain(ctx, state.ID.ValueInt64(), idemKey); err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) && apiErr.Type == "not_found_error" {
			return // Already gone — converged.
		}
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to delete domain", err)
		return
	}
}

func (r *DomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected a numeric domain id (e.g. `terraform import scrutari_domain.example 17`), got %q: %s", req.ID, err),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
