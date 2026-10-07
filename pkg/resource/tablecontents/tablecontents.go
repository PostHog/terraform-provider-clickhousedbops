package tablecontents

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

//go:embed tablecontents.md
var description string

var (
	_ resource.Resource               = &Resource{}
	_ resource.ResourceWithConfigure  = &Resource{}
	_ resource.ResourceWithModifyPlan = &Resource{}
)

func NewResource() resource.Resource {
	return &Resource{}
}

type Resource struct {
	client dbops.Client
}

type model struct {
	SQLPlanDigest types.String `tfsdk:"sql_plan_digest"`
	ID            types.String `tfsdk:"id"`
	Database      types.String `tfsdk:"database"`
	Table         types.String `tfsdk:"table"`
	Format        types.String `tfsdk:"format"`
	Data          types.String `tfsdk:"data"`
	Checksum      types.String `tfsdk:"checksum"`
	Node          types.Object `tfsdk:"node"`
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_table_contents"
}

func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	resp.Schema = schema.Schema{
		MarkdownDescription: description,
		Attributes: map[string]schema.Attribute{
			"sql_plan_digest": schema.StringAttribute{Computed: true, Description: "Digest of the reviewed SQL operations."},
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "database.table",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"database": schema.StringAttribute{
				Required:      true,
				Description:   "Database of the table.",
				Validators:    nonEmpty,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"table": schema.StringAttribute{
				Required:      true,
				Description:   "Table whose rows this resource declares. It must be a MergeTree or ReplicatedMergeTree table.",
				Validators:    nonEmpty,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"format": schema.StringAttribute{
				Required:    true,
				Description: "ClickHouse input format of data, for example JSONEachRow or CSVWithNames.",
				Validators:  nonEmpty,
			},
			"data": schema.StringAttribute{
				Required:    true,
				Description: "Every row the table holds, in format. Columns that data leaves out take their defaults; MATERIALIZED columns are computed.",
			},
			"checksum": schema.StringAttribute{
				Computed:    true,
				Description: "Row count and an order-independent hash of the rows. In state it is what the table holds; in the plan it is what data holds.",
			},
			"node": schemahelpers.NodeAttribute("table contents"),
		},
	}
}

func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.client = req.ProviderData.(dbops.Client)
}

// ModifyPlan plans the checksum of the declared data, so that a table whose rows differ from it
// shows as a change, and data that only formats the same rows differently does not.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.Plan.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Data.IsUnknown() || plan.Format.IsUnknown() || plan.Database.IsUnknown() || plan.Table.IsUnknown() {
		return
	}

	desired, err := r.client.DataChecksum(ctx, plan.Database.ValueString(), plan.Table.ValueString(), plan.Format.ValueString(), plan.Data.ValueString())
	if err != nil && !errors.Is(err, dbops.ErrTableNotFound) {
		resp.Diagnostics.AddError("Error reading the declared data", err.Error())
		return
	}
	if err != nil {
		// A table that this plan creates does not exist yet; apply computes the checksum.
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("checksum"), types.StringUnknown())...)
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("checksum"), types.StringValue(desired))...)

	if req.State.Raw.IsNull() {
		return
	}
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Checksum.ValueString() == desired && !state.Database.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("data"), state.Data)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("format"), state.Format)...)
	}
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.Plan.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.write(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.Plan.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.write(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.State.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	checksum, exists, diags := r.checksum(ctx, state.Database.ValueString(), state.Table.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !exists {
		resp.State.RemoveResource(ctx)
		return
	}
	state.Checksum = types.StringValue(checksum)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete leaves the rows in place: no longer declaring the contents is not a request to empty
// the table. Dropping the table removes them.
func (r *Resource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}

// write replaces the rows of the table with the data, then checks that the table holds exactly
// the data.
func (r *Resource) write(ctx context.Context, plan *model) diag.Diagnostics {
	var diags diag.Diagnostics
	database, table := plan.Database.ValueString(), plan.Table.ValueString()

	if err := r.client.ReplaceTableContents(ctx, database, table, plan.Format.ValueString(), plan.Data.ValueString()); err != nil {
		diags.AddError(fmt.Sprintf("Error writing the contents of %s.%s", database, table), fmt.Sprintf("%+v\n", err))
		return diags
	}

	if clickhouseclient.IsRecordingSQL(ctx) {
		plan.ID = types.StringValue(database + "." + table)
		return diags
	}

	desired, err := r.client.DataChecksum(ctx, database, table, plan.Format.ValueString(), plan.Data.ValueString())
	if err != nil {
		diags.AddError("Error reading the declared data", fmt.Sprintf("%+v\n", err))
		return diags
	}
	actual, _, readDiags := r.checksum(ctx, database, table)
	diags.Append(readDiags...)
	if diags.HasError() {
		return diags
	}
	if actual != desired {
		diags.AddError(
			fmt.Sprintf("The contents of %s.%s do not match the data after writing", database, table),
			fmt.Sprintf("The data has checksum %s and the table %s. An engine that merges or deduplicates rows, or rows the data repeats, cause this.", desired, actual),
		)
		return diags
	}
	plan.ID = types.StringValue(database + "." + table)
	plan.Checksum = types.StringValue(actual)
	return diags
}

// checksum returns the checksum of the table's rows, and whether the table exists.
func (r *Resource) checksum(ctx context.Context, database string, table string) (string, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	checksum, exists, err := r.client.TableContentsChecksum(ctx, database, table)
	if err != nil {
		diags.AddError(fmt.Sprintf("Error reading the contents of %s.%s", database, table), fmt.Sprintf("%+v\n", err))
	}
	return checksum, exists, diags
}
