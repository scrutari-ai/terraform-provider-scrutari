package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	gwclient "github.com/scrutari-ai/terraform-provider-scrutari/internal/client"
)

var (
	_ resource.Resource                = &PiiPolicyResource{}
	_ resource.ResourceWithConfigure   = &PiiPolicyResource{}
	_ resource.ResourceWithImportState = &PiiPolicyResource{}
)

// The synthetic ID of the singleton document. One per tenant, the
// tenant is derived from the API key, so there is nothing else to
// identify: `terraform import scrutari_pii_policy.this tenant`.
const piiPolicyID = "tenant"

func NewPiiPolicyResource() resource.Resource {
	return &PiiPolicyResource{}
}

type PiiPolicyResource struct {
	client *Client
}

// PiiPolicyResourceModel is the document. `Rule` is the set of NON-default
// rules; a category not listed is `audit` (the safe default), so the
// resource never carries `audit` rows. `UpdatedAt` echoes the last write.
type PiiPolicyResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Rule      types.Set    `tfsdk:"rule"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// piiRuleModel is one `rule { category, mode }` block.
type piiRuleModel struct {
	Category types.String `tfsdk:"category"`
	Mode     types.String `tfsdk:"mode"`
}

// piiRuleObjectType is the element type of the `rule` set, kept in one
// place so Read's SetValueFrom and the schema agree.
var piiRuleObjectType = types.ObjectType{
	AttrTypes: map[string]attr.Type{
		"category": types.StringType,
		"mode":     types.StringType,
	},
}

func (r *PiiPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pii_policy"
}

func (r *PiiPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The tenant's PII redaction policy for AI traffic. Before a prompt reaches an upstream AI " +
			"provider, the gateway scans it for standard PII (SSNs, credit cards, emails, phone numbers, and healthcare " +
			"identifiers) and acts per this policy. One document per tenant: model it as exactly one resource instance.\n\n" +
			"**Applies to AI routes only.** Non-AI passthrough traffic is never scanned, so this control adds no latency " +
			"to the bulk proxy path.\n\n" +
			"**Replace semantics.** The `rule` blocks are the COMPLETE set of non-default rules: any of the eight " +
			"categories you do not declare reverts to the safe `audit` default (detect and log, forward untouched). " +
			"Declare only the categories you want to `redact` or `block`; `audit` is the absence of a rule.\n\n" +
			"**Destroy resets, it does not delete**: `terraform destroy` writes the all-audit default back and removes " +
			"the resource from state. A write takes effect immediately on every AI-path scan.\n\n" +
			"The `pii_policy:write` scope this resource needs is owner/admin-tier (the data-loss-prevention capability).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Always the literal `tenant` (the document is a per-tenant singleton). Import key: `terraform import scrutari_pii_policy.this tenant`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "RFC 3339 timestamp of the last write, or null when the document is the all-audit default.",
				Computed:            true,
			},
		},
		Blocks: map[string]schema.Block{
			"rule": schema.SetNestedBlock{
				MarkdownDescription: "A category to enforce above the `audit` default. Omit a category to leave it at `audit`.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"category": schema.StringAttribute{
							MarkdownDescription: "One of: `ssn`, `us_phone`, `email`, `credit_card`, `mrn`, `npi`, `icd10`, `dob`.",
							Required:            true,
							Validators: []validator.String{
								stringvalidator.OneOf("ssn", "us_phone", "email", "credit_card", "mrn", "npi", "icd10", "dob"),
							},
						},
						"mode": schema.StringAttribute{
							MarkdownDescription: "`redact` (replace the matched text before forwarding) or `block` (refuse the request with HTTP 422). " +
								"`audit` is the default and is expressed by omitting the rule, not by a `mode = \"audit\"` value.",
							Required: true,
							Validators: []validator.String{
								stringvalidator.OneOf("redact", "block"),
							},
						},
					},
				},
			},
		},
	}
}

func (r *PiiPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// put writes the document and re-hydrates the model from the response.
// Create and Update are byte-identical PUTs by design (the gateway
// replaces the whole document); the split exists only for Terraform's
// lifecycle.
func (r *PiiPolicyResource) put(ctx context.Context, plan *PiiPolicyResourceModel, operation string, diags interface {
	AddError(string, string)
	AddWarning(string, string)
}) bool {
	var rules []piiRuleModel
	if d := plan.Rule.ElementsAs(ctx, &rules, false); d.HasError() {
		for _, e := range d.Errors() {
			diags.AddError(e.Summary(), e.Detail())
		}
		return false
	}

	apiRules := make([]gwclient.PiiPolicyRule, 0, len(rules))
	for _, rule := range rules {
		apiRules = append(apiRules, gwclient.PiiPolicyRule{
			Category: rule.Category.ValueString(),
			Mode:     rule.Mode.ValueString(),
		})
	}
	apiReq := gwclient.PutPiiPolicyRequest{Policies: apiRules}

	idemKey := r.client.IdempotencyKey("scrutari_pii_policy."+piiPolicyID, operation)
	doc, err := r.client.PutPiiPolicy(ctx, apiReq, idemKey)
	if err != nil {
		appendAPIErrorDiag(diags, "Failed to write PII policy", err)
		return false
	}
	return hydratePiiPolicyModel(ctx, plan, doc, diags)
}

func (r *PiiPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PiiPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.put(ctx, &plan, "create", &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *PiiPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PiiPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	doc, err := r.client.GetPiiPolicy(ctx)
	if err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to read PII policy", err)
		return
	}
	// The document always exists (the gateway fabricates the all-audit
	// default), so there is no RemoveResource arm: a default-shaped read
	// of a managed resource is honest drift for Terraform to reconcile,
	// not a deletion.
	if !hydratePiiPolicyModel(ctx, &state, doc, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *PiiPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PiiPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.put(ctx, &plan, "update", &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete resets the document to the all-audit default: the document
// conceptually always exists, so "destroy" means "stop enforcing", and
// the audit trail stays linear (an explicit reset is forensically
// distinguishable from never-touched). An empty policies array clears
// every non-audit rule.
func (r *PiiPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PiiPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := gwclient.PutPiiPolicyRequest{Policies: []gwclient.PiiPolicyRule{}}
	idemKey := r.client.IdempotencyKey("scrutari_pii_policy."+piiPolicyID, "delete")
	if _, err := r.client.PutPiiPolicy(ctx, apiReq, idemKey); err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to reset PII policy on destroy", err)
		return
	}
}

func (r *PiiPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != piiPolicyID {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf(
				"The PII policy is a per-tenant singleton; import it with the literal id %q "+
					"(e.g. `terraform import scrutari_pii_policy.this tenant`), got %q.",
				piiPolicyID, req.ID,
			),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), piiPolicyID)...)
}

// hydratePiiPolicyModel writes the API response onto the model. Only the
// non-audit rules land in `rule`, so state matches what the operator
// declares (audit categories are the absence of a rule).
func hydratePiiPolicyModel(ctx context.Context, m *PiiPolicyResourceModel, doc *gwclient.PiiPolicyResponse, diags interface {
	AddError(string, string)
	AddWarning(string, string)
}) bool {
	m.ID = types.StringValue(piiPolicyID)
	m.UpdatedAt = types.StringPointerValue(doc.UpdatedAt)

	rules := make([]piiRuleModel, 0, len(doc.Policies))
	for _, p := range doc.Policies {
		if p.Mode == "audit" {
			continue
		}
		rules = append(rules, piiRuleModel{
			Category: types.StringValue(p.Category),
			Mode:     types.StringValue(p.Mode),
		})
	}

	set, d := types.SetValueFrom(ctx, piiRuleObjectType, rules)
	if d.HasError() {
		for _, e := range d.Errors() {
			diags.AddError(e.Summary(), e.Detail())
		}
		return false
	}
	m.Rule = set
	return true
}
