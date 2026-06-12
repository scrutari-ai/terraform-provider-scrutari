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
	_ resource.Resource                = &TransportPolicyResource{}
	_ resource.ResourceWithConfigure   = &TransportPolicyResource{}
	_ resource.ResourceWithImportState = &TransportPolicyResource{}
)

// The synthetic ID of the singleton document. One per tenant, the
// tenant is derived from the API key, so there is nothing else to
// identify — `terraform import scrutari_transport_policy.this tenant`.
const transportPolicyID = "tenant"

func NewTransportPolicyResource() resource.Resource {
	return &TransportPolicyResource{}
}

type TransportPolicyResource struct {
	client *Client
}

type TransportPolicyResourceModel struct {
	ID             types.String `tfsdk:"id"`
	MinPosture     types.String `tfsdk:"min_posture"`
	Mode           types.String `tfsdk:"mode"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
	AttestableHere types.Bool   `tfsdk:"attestable_here"`
}

func (r *TransportPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_transport_policy"
}

func (r *TransportPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The tenant's transport policy (RFC-012): a cryptographic FLOOR over the posture grades " +
			"(`any` < `hybrid` < `cnsa-2.0`) with a `monitor` or `enforce` mode. One document per tenant — model it as " +
			"exactly one resource instance.\n\n" +
			"**Enforcement is attestation-based and fail-closed.** The gateway only enforces floors its deployment " +
			"posture can PROVE per connection; applying `mode = \"enforce\"` with a floor this deployment cannot attest " +
			"fails with `transport_policy_not_attestable` (the message names what the deployment proves). " +
			"`mode = \"monitor\"` accepts any floor and records verdicts and violations without affecting traffic — " +
			"start there, watch the violation events, then switch to enforce.\n\n" +
			"**Destroy resets, it does not delete**: the document conceptually always exists, so `terraform destroy` " +
			"writes the default policy (`any` / `monitor`) back and removes the resource from state. A policy write " +
			"converges on every gateway pod within 60 seconds.\n\n" +
			"The `transport_policy:write` scope this resource needs is owner/admin-tier. Like zones, consider keeping " +
			"it in a separate, rarely-run configuration: posture floors change on compliance cadence, not deploy cadence.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Always the literal `tenant` (the document is a per-tenant singleton). Import key: `terraform import scrutari_transport_policy.this tenant`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"min_posture": schema.StringAttribute{
				MarkdownDescription: "The floor: `any` (no tenant-imposed constraint), `hybrid` (at least hybrid post-quantum " +
					"key exchange, X25519MLKEM768), or `cnsa-2.0` (Category 5, ML-KEM-1024).",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf("any", "hybrid", "cnsa-2.0"),
				},
			},
			"mode": schema.StringAttribute{
				MarkdownDescription: "`monitor` (default: record verdicts and publish violations, never affect traffic) or " +
					"`enforce` (refuse non-conforming connections with HTTP 426; requires the deployment to attest the floor).",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("monitor"),
				Validators: []validator.String{
					stringvalidator.OneOf("monitor", "enforce"),
				},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of the last write, or null when the document has never been written.",
				Computed:            true,
			},
			"attestable_here": schema.BoolAttribute{
				MarkdownDescription: "Whether the deployment serving this tenant can PROVE connections meet the floor. " +
					"When false, verdicts are recorded as `unknowable` and `mode = \"enforce\"` would be refused.",
				Computed: true,
			},
		},
	}
}

func (r *TransportPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// put writes the document and hydrates the model from the response.
// Create and Update are byte-identical PUTs by design (the gateway
// upserts); the split exists only for Terraform's lifecycle.
func (r *TransportPolicyResource) put(ctx context.Context, plan *TransportPolicyResourceModel, operation string, diags interface {
	AddError(string, string)
	AddWarning(string, string)
}) bool {
	mode := plan.Mode.ValueString()
	apiReq := gwclient.PutTransportPolicyRequest{
		MinPosture: plan.MinPosture.ValueString(),
		Mode:       &mode,
	}
	idemKey := r.client.IdempotencyKey("scrutari_transport_policy."+transportPolicyID, operation)
	doc, err := r.client.PutTransportPolicy(ctx, apiReq, idemKey)
	if err != nil {
		appendAPIErrorDiag(diags, "Failed to write transport policy", err)
		return false
	}
	hydrateTransportPolicyModel(plan, doc)
	return true
}

func (r *TransportPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan TransportPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.put(ctx, &plan, "create", &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *TransportPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state TransportPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	doc, err := r.client.GetTransportPolicy(ctx)
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to read transport policy", err)
		return
	}
	// The document always exists (the gateway fabricates the default),
	// so there is no RemoveResource arm here: a default-shaped read of
	// a managed resource is honest drift for Terraform to reconcile,
	// not a deletion.
	hydrateTransportPolicyModel(&state, doc)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *TransportPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan TransportPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.put(ctx, &plan, "update", &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete resets the document to the default policy (D7): the
// document conceptually always exists, so "destroy" means "stop
// imposing a floor", and the audit trail stays linear (an explicit
// reset is forensically distinguishable from never-touched).
func (r *TransportPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state TransportPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	mode := "monitor"
	apiReq := gwclient.PutTransportPolicyRequest{
		MinPosture: "any",
		Mode:       &mode,
	}
	idemKey := r.client.IdempotencyKey("scrutari_transport_policy."+transportPolicyID, "delete")
	if _, err := r.client.PutTransportPolicy(ctx, apiReq, idemKey); err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to reset transport policy on destroy", err)
		return
	}
}

func (r *TransportPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != transportPolicyID {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf(
				"The transport policy is a per-tenant singleton; import it with the literal id %q "+
					"(e.g. `terraform import scrutari_transport_policy.this tenant`), got %q.",
				transportPolicyID, req.ID,
			),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), transportPolicyID)...)
}

func hydrateTransportPolicyModel(m *TransportPolicyResourceModel, doc *gwclient.TransportPolicyResponse) {
	m.ID = types.StringValue(transportPolicyID)
	m.MinPosture = types.StringValue(doc.MinPosture)
	m.Mode = types.StringValue(doc.Mode)
	m.UpdatedAt = types.StringPointerValue(doc.UpdatedAt)
	m.AttestableHere = types.BoolValue(doc.AttestableHere)
}
