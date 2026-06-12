package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	gwclient "github.com/scrutari-ai/terraform-provider-scrutari/internal/client"
)

var (
	_ resource.Resource                = &DelegatedZoneResource{}
	_ resource.ResourceWithConfigure   = &DelegatedZoneResource{}
	_ resource.ResourceWithImportState = &DelegatedZoneResource{}
)

func NewDelegatedZoneResource() resource.Resource {
	return &DelegatedZoneResource{}
}

type DelegatedZoneResource struct {
	client *Client
}

// DelegatedZoneResourceModel — the zone's registration plus the
// server-driven lifecycle fields Terraform refreshes on read. The
// status machine is gateway-owned (pending_verification →
// verifying_delegation → active, with degraded / offboarding /
// revoked around it); Terraform observes, it never drives.
type DelegatedZoneResourceModel struct {
	ID             types.String `tfsdk:"id"`
	Zone           types.String `tfsdk:"zone"`
	DelegationMode types.String `tfsdk:"delegation_mode"`
	Status         types.String `tfsdk:"status"`
	ChallengeHost  types.String `tfsdk:"challenge_host"`
	ExpectedTxt    types.String `tfsdk:"expected_txt"`
	NameServers    types.List   `tfsdk:"name_servers"`
	CreatedAt      types.String `tfsdk:"created_at"`
	ActivatedAt    types.String `tfsdk:"activated_at"`
}

func (r *DelegatedZoneResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_delegated_zone"
}

func (r *DelegatedZoneResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A delegated wildcard zone (RFC-010): delegate `ai.example.com` to Scrutari-operated name servers " +
			"once, and every hostname under it (created via `scrutari_domain` with `zone_id`) is born verified with no " +
			"per-name DNS work — one wildcard certificate per zone covers traffic.\n\n" +
			"**Activation is a two-step DNS handshake that Terraform starts but cannot finish alone**: (1) apply returns the " +
			"one-time parent-zone TXT challenge (`challenge_host` + `expected_txt`); once it is published and verified, the " +
			"gateway provisions name servers and `name_servers` fills on the next refresh; (2) publish those NS records in the " +
			"parent zone and the zone flips `active`. Hostnames under the zone can only be created while it is `active`.\n\n" +
			"**Destroy starts offboarding, it does not instantly delete**: the gateway drains hostnames and routes, revokes and " +
			"purges the wildcard certificate, and deletes its DNS zone only after your NS records stop resolving to Scrutari " +
			"(the takeover guard). The zone NAME is tombstoned: re-registering it always requires a fresh parent-zone TXT " +
			"proof — plan zone names accordingly.\n\n" +
			"The `zones:write` scope this resource needs is the gateway's most powerful scope. Keep it out of routine " +
			"pipelines: zones change yearly, hostnames change daily — split your Terraform configurations along that line.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Zone ID (UUID) assigned by the gateway. Import key: `terraform import scrutari_delegated_zone.example <uuid>`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"zone": schema.StringAttribute{
				MarkdownDescription: "The zone to delegate, e.g. `ai.example.com`. At least three labels (apex delegation is not " +
					"supported). Changing it replaces the resource — and the OLD name stays tombstoned server-side.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(zoneRegex, "must be a bare zone of at least three labels (e.g. ai.example.com)"),
				},
			},
			"delegation_mode": schema.StringAttribute{
				MarkdownDescription: "`ns` (primary, default) or `cname_pair` (fallback for organizations that cannot delegate NS).",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("ns"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("ns", "cname_pair"),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Gateway-owned lifecycle status: `pending_verification` → `verifying_delegation` → `active`, " +
					"plus `degraded` (delegation stopped resolving; self-heals when it returns), `offboarding`, and `revoked`.",
				Computed: true,
			},
			"challenge_host": schema.StringAttribute{
				MarkdownDescription: "DNS name to publish the one-time proof TXT at, in the PARENT zone (the delegation is not live yet).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"expected_txt": schema.StringAttribute{
				MarkdownDescription: "The exact TXT value proving parent-zone control. **Delivered exactly once, on create**: reads " +
					"never return it, so it lives only in Terraform state (treat your state accordingly) and an imported " +
					"resource has it null.",
				Computed:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name_servers": schema.ListAttribute{
				MarkdownDescription: "The NS records to publish in the parent zone. Empty until the parent-zone TXT proof verifies " +
					"and the gateway provisions its DNS zone — re-run `terraform refresh`/`plan` after publishing the TXT to pick them up.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp when the zone was registered.",
				Computed:            true,
			},
			"activated_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp when the zone reached `active`, or null while the delegation handshake is incomplete.",
				Computed:            true,
			},
		},
	}
}

func (r *DelegatedZoneResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *DelegatedZoneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DelegatedZoneResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	mode := plan.DelegationMode.ValueString()
	apiReq := gwclient.RegisterZoneRequest{
		Zone:           plan.Zone.ValueString(),
		DelegationMode: &mode,
	}

	idemKey := r.client.IdempotencyKey("scrutari_delegated_zone."+plan.Zone.ValueString(), "create")
	registered, err := r.client.RegisterZone(ctx, apiReq, idemKey)
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to register delegated zone", err)
		return
	}

	plan.ID = types.StringValue(registered.Id)
	plan.Zone = types.StringValue(registered.Zone)
	plan.DelegationMode = types.StringValue(registered.DelegationMode)
	plan.Status = types.StringValue(registered.Status)
	plan.ChallengeHost = types.StringValue(registered.ChallengeHost)
	// The one-time parent-zone proof. Reads never return it; this
	// assignment is the only place it ever enters state.
	plan.ExpectedTxt = types.StringValue(registered.ExpectedTxt)

	// The register response is the challenge envelope, not the full
	// row — follow with one GET so `created_at` / `name_servers`
	// land as known values rather than unknowns, keeping the
	// post-apply state identical in shape to every later refresh.
	row, err := r.client.GetZone(ctx, registered.Id)
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Zone registered but the follow-up read failed", err)
		return
	}
	hydrateZoneModel(ctx, &plan, row, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *DelegatedZoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DelegatedZoneResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	row, err := r.client.GetZone(ctx, state.ID.ValueString())
	if err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) && apiErr.Type == "not_found_error" {
			resp.State.RemoveResource(ctx)
			return
		}
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to read delegated zone", err)
		return
	}

	// A zone that finished offboarding (drain complete → 'revoked'
	// tombstone) is operationally GONE even though its tombstone row
	// remains readable. Dropping it from state lets `terraform apply`
	// converge on re-creation — which the gateway will only accept
	// with a fresh parent-zone TXT proof, exactly the §6 contract.
	if row.Status == "revoked" {
		resp.State.RemoveResource(ctx)
		return
	}

	// `expected_txt` is deliberately NOT touched: create-only secret,
	// reads never return it (same posture as scrutari_domain's
	// expected_value and the API key plaintext).
	hydrateZoneModel(ctx, &state, row, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is structurally unreachable: `zone` and `delegation_mode`
// both carry RequiresReplace and every other attribute is Computed.
// Fail loud if a schema edit ever breaks that invariant.
func (r *DelegatedZoneResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"In-place update is not supported for scrutari_delegated_zone",
		"All mutable attributes require replacement; this code path should be unreachable. Please report this issue.",
	)
}

// Delete starts the gateway-side offboarding drain and returns. The
// 202 contract: hostname rows and routes are cascaded, the wildcard
// cert is revoked and purged, and the Scrutari-side DNS zone is
// deleted only once the customer's NS records stop resolving to
// Scrutari. Terraform's job ends at starting that process — blocking
// the apply on a step only a human DNS change can unblock would hang
// every destroy behind an out-of-band action.
func (r *DelegatedZoneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DelegatedZoneResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idemKey := r.client.IdempotencyKey("scrutari_delegated_zone.id."+state.ID.ValueString(), "delete")
	out, err := r.client.OffboardZone(ctx, state.ID.ValueString(), idemKey)
	if err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) {
			switch {
			case apiErr.Type == "not_found_error":
				return // Row vanished server-side — converged.
			case apiErr.Code == "zone_already_revoked":
				return // Drain already completed — converged.
			}
		}
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to start zone offboarding", err)
		return
	}

	// Surface the one step only the operator can perform. A warning,
	// not an error: the destroy SUCCEEDED, the reminder is about the
	// server-side drain it started.
	resp.Diagnostics.AddWarning(
		fmt.Sprintf("Zone %s is offboarding (not yet deleted)", out.Zone),
		out.NextSteps,
	)
}

func (r *DelegatedZoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !uuidRegex.MatchString(req.ID) {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected a zone UUID (e.g. `terraform import scrutari_delegated_zone.example 3f8a3a1e-...`), got %q", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// hydrateZoneModel refreshes every server-owned field from a
// ZoneResponse. `expected_txt` is never touched here (create-only;
// see Read). Nullable timestamps map onto Terraform nulls via
// StringPointerValue — an un-activated zone honestly shows
// `activated_at = null` rather than "".
func hydrateZoneModel(ctx context.Context, m *DelegatedZoneResourceModel, row *gwclient.ZoneResponse, diags interface {
	AddError(string, string)
}) {
	m.ID = types.StringValue(row.Id)
	m.Zone = types.StringValue(row.Zone)
	m.DelegationMode = types.StringValue(row.DelegationMode)
	m.Status = types.StringValue(row.Status)
	m.CreatedAt = types.StringPointerValue(row.CreatedAt)
	m.ActivatedAt = types.StringPointerValue(row.ActivatedAt)

	nameServers, listDiags := types.ListValueFrom(ctx, types.StringType, row.NameServers)
	if listDiags.HasError() {
		// Structurally impossible for []string → list(string); keep
		// the loud path anyway so a generated-type change surfaces.
		diags.AddError(
			"Failed to convert name_servers",
			fmt.Sprintf("could not build list value from %v", row.NameServers),
		)
		return
	}
	m.NameServers = nameServers
}
