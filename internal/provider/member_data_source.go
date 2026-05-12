package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	// Generated wire DTOs — see `client.go` route comment block for
	// the hybrid-architecture rationale.
	gwclient "github.com/scrutari-ai/terraform-provider-scrutari/internal/client"
)

var (
	_ datasource.DataSource              = &MemberDataSource{}
	_ datasource.DataSourceWithConfigure = &MemberDataSource{}
)

func NewMemberDataSource() datasource.DataSource {
	return &MemberDataSource{}
}

type MemberDataSource struct {
	client *Client
}

type MemberDataSourceModel struct {
	UserID      types.String `tfsdk:"user_id"`
	TenantID    types.String `tfsdk:"tenant_id"`
	Role        types.String `tfsdk:"role"`
	Email       types.String `tfsdk:"email"`
	DisplayName types.String `tfsdk:"display_name"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

// Local wire DTO removed; the gateway's `MemberResponse` is now
// imported from `gwclient`. Worth flagging on the contract surface:
// the gateway-side `repo::list` filters out deactivated members, so
// a 404 here can mean either "user_id not in this tenant" OR
// "user_id was deactivated" — same anti-disclosure posture as
// the rest of the API.

func (d *MemberDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_member"
}

func (d *MemberDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a single tenant member by user_id (UUID). Deactivated members return 404 — same anti-disclosure posture as the rest of the API.",
		Attributes: map[string]schema.Attribute{
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Member's user UUID (e.g. `550e8400-e29b-41d4-a716-446655440000`).",
				Required:            true,
				Validators: []validator.String{
					// Hyphenated 36-char UUID. The gateway parses this
					// strictly; pre-validating keeps the error
					// path-friendly (400 from the gateway has the same
					// message but slower turnaround).
					stringvalidator.LengthBetween(36, 36),
					stringvalidator.RegexMatches(
						uuidRegex,
						"must be a hyphenated UUID (e.g. 550e8400-e29b-41d4-a716-446655440000)",
					),
				},
			},
			"tenant_id":    schema.StringAttribute{Computed: true},
			"role":         schema.StringAttribute{Computed: true, MarkdownDescription: "`owner` / `admin` / `operator` / `viewer` / `auditor`."},
			"email":        schema.StringAttribute{Computed: true},
			"display_name": schema.StringAttribute{Computed: true},
			"created_at":   schema.StringAttribute{Computed: true},
			"updated_at":   schema.StringAttribute{Computed: true},
		},
	}
}

func (d *MemberDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *MemberDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config MemberDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	userID := config.UserID.ValueString()
	var row gwclient.MemberResponse
	if err := d.client.do(ctx, "GET", fmt.Sprintf("/v1/members/%s", userID), nil, &row, ""); err != nil {
		appendAPIErrorDiag(&resp.Diagnostics, "Failed to read member", err)
		return
	}

	// Field-name notes (oapi-codegen CamelCase): UserId / TenantId
	// (not UserID / TenantID). All seven fields are `required` on
	// the gateway-side schema, so they come back as bare types — no
	// pointer-deref dance needed here.
	model := MemberDataSourceModel{
		UserID:      types.StringValue(row.UserId),
		TenantID:    types.StringValue(row.TenantId),
		Role:        types.StringValue(row.Role),
		Email:       types.StringValue(row.Email),
		DisplayName: types.StringValue(row.DisplayName),
		CreatedAt:   types.StringValue(row.CreatedAt),
		UpdatedAt:   types.StringValue(row.UpdatedAt),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}
