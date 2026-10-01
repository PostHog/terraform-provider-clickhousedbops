package tablecontents

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
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
	ID       types.String `tfsdk:"id"`
	Database types.String `tfsdk:"database"`
	Table    types.String `tfsdk:"table"`
	Format   types.String `tfsdk:"format"`
	Data     types.String `tfsdk:"data"`
	Checksum types.String `tfsdk:"checksum"`
}

// A checksum that no data can have, for a table whose nodes disagree.
const nodesDiffer = "nodes differ"

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_table_contents"
}

func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	resp.Schema = schema.Schema{
		MarkdownDescription: description,
		Attributes: map[string]schema.Attribute{
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
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	checksum, exists, diags := r.nodesChecksum(ctx, state.Database.ValueString(), state.Table.ValueString())
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

// write loads the data on every node that holds its own copy of the rows: each node of a
// non-replicated table, and one replica per shard of a Replicated one. It then checks that every
// node holds exactly the data.
func (r *Resource) write(ctx context.Context, plan *model) diag.Diagnostics {
	var diags diag.Diagnostics
	database, table := plan.Database.ValueString(), plan.Table.ValueString()

	nodes, err := r.client.SchemaNodes(ctx)
	if err != nil {
		diags.AddError("Error listing cluster nodes", fmt.Sprintf("%+v\n", err))
		return diags
	}
	written := map[string]bool{}
	for _, node := range nodes {
		replicationPath, err := node.Client.ReplicationPath(ctx, database, table)
		if err != nil {
			diags.AddError(fmt.Sprintf("Error reading %s.%s on node %q", database, table, node.Host), fmt.Sprintf("%+v\n", err))
			return diags
		}
		if replicationPath != "" && written[replicationPath] {
			continue
		}
		if err := node.Client.ReplaceTableContents(ctx, database, table, plan.Format.ValueString(), plan.Data.ValueString()); err != nil {
			diags.AddError(fmt.Sprintf("Error writing the contents of %s.%s on node %q", database, table, node.Host), fmt.Sprintf("%+v\n", err))
			return diags
		}
		written[replicationPath] = true
	}

	desired, err := r.client.DataChecksum(ctx, database, table, plan.Format.ValueString(), plan.Data.ValueString())
	if err != nil {
		diags.AddError("Error reading the declared data", fmt.Sprintf("%+v\n", err))
		return diags
	}
	actual, _, readDiags := r.nodesChecksum(ctx, database, table)
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

// nodesChecksum returns the checksum every node agrees on, or nodesDiffer.
func (r *Resource) nodesChecksum(ctx context.Context, database string, table string) (string, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	nodes, err := r.client.SchemaNodes(ctx)
	if err != nil {
		diags.AddError("Error listing cluster nodes", fmt.Sprintf("%+v\n", err))
		return "", false, diags
	}
	byChecksum := map[string][]string{}
	var missing []string
	for _, node := range nodes {
		checksum, exists, err := node.Client.TableContentsChecksum(ctx, database, table)
		if err != nil {
			diags.AddError(fmt.Sprintf("Error reading the contents of %s.%s on node %q", database, table, node.Host), fmt.Sprintf("%+v\n", err))
			return "", false, diags
		}
		if !exists {
			missing = append(missing, node.Host)
			continue
		}
		byChecksum[checksum] = append(byChecksum[checksum], node.Host)
	}
	if len(byChecksum) == 0 {
		return "", false, diags
	}
	if len(byChecksum) == 1 && len(missing) == 0 {
		for checksum := range byChecksum {
			return checksum, true, diags
		}
	}
	var groups []string
	if len(missing) > 0 {
		groups = append(groups, "table missing on "+strings.Join(missing, ", "))
	}
	for checksum, hosts := range byChecksum {
		groups = append(groups, fmt.Sprintf("%s on %s", checksum, strings.Join(hosts, ", ")))
	}
	sort.Strings(groups)
	diags.AddWarning(fmt.Sprintf("The nodes hold different contents of %s.%s", database, table), strings.Join(groups, "; "))
	return nodesDiffer, true, diags
}
