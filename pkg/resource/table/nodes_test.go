package table

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

type tableNodeClient struct {
	dbops.Client
	nodes      []dbops.SchemaNode
	table      *dbops.Table
	replicated []*tableNodeClient
	alters     int
	rows       uint64
}

func (c *tableNodeClient) SchemaNodes(context.Context) ([]dbops.SchemaNode, error) {
	return c.nodes, nil
}
func (c *tableNodeClient) FanoutCluster() string   { return "example" }
func (c *tableNodeClient) IgnoreColumnOrder() bool { return true }
func (c *tableNodeClient) GetTable(context.Context, string, string, *string) (*dbops.Table, error) {
	copy := *c.table
	return &copy, nil
}

func (c *tableNodeClient) GetTableEngineCapabilities(context.Context, string) (dbops.TableEngineCapabilities, error) {
	return dbops.TableEngineCapabilities{Name: "MergeTree", Known: true, SupportsSortOrder: true, SupportsSettings: true, SupportsTTL: true}, nil
}

func (c *tableNodeClient) GetTableSettingCapabilities(context.Context, string, []string) (map[string]dbops.TableSettingCapability, error) {
	return nil, nil
}

func (c *tableNodeClient) AlterTable(_ context.Context, _ string, _ string, _ *string, actions []string) ([]dbops.RunningMutation, error) {
	c.alters++
	for _, action := range actions {
		if strings.HasPrefix(action, "ADD COLUMN") {
			for _, replica := range c.replicated {
				replica.table.Columns = append(replica.table.Columns, dbops.Column{Name: "added", Type: "UInt64"})
			}
		}
	}
	return nil, nil
}

func plannedTable(t *testing.T, r *Resource, engine string) (TableResourceModel, tfsdk.State) {
	t.Helper()
	ctx := context.Background()
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	columns, diags := schemahelpers.ColumnsValue(ctx, []dbops.Column{{Name: "id", Type: "UInt64"}, {Name: "added", Type: "UInt64"}})
	if diags.HasError() {
		t.Fatal(diags)
	}
	nodes, diags := types.ListValueFrom(ctx, types.StringType, []string{"node1", "node2", "node3"})
	if diags.HasError() {
		t.Fatal(diags)
	}
	m := TableResourceModel{Database: types.StringValue("example"), Name: types.StringValue("t"), Engine: types.StringValue(engine), Columns: columns, OrderBy: types.StringValue("id"), Nodes: nodes, CreateStatement: types.StringValue("CREATE TABLE example.t"), ForceDestroy: types.BoolValue(true)}
	for name, target := range map[string]*types.List{"indexes": &m.Indexes, "projections": &m.Projections, "constraints": &m.Constraints, "unmanaged_columns": &m.UnmanagedColumns, "unmanaged_indexes": &m.UnmanagedIndexes} {
		listType := sr.Schema.Attributes[name].GetType().(types.ListType)
		*target = types.ListNull(listType.ElemType)
	}
	state := tfsdk.State{Schema: sr.Schema}
	if diags := state.Set(ctx, m); diags.HasError() {
		t.Fatal(diags)
	}
	return m, state
}

func TestPlanAggregatesNodeDriftAndReplacement(t *testing.T) {
	for _, immutable := range []bool{false, true} {
		first := &tableNodeClient{table: &dbops.Table{Engine: "MergeTree()", Columns: []dbops.Column{{Name: "id", Type: "UInt64"}, {Name: "added", Type: "UInt64"}}, OrderBy: "id"}}
		second := &tableNodeClient{table: &dbops.Table{Engine: "MergeTree()", Columns: []dbops.Column{{Name: "id", Type: "UInt64"}}, OrderBy: "id"}}
		third := &tableNodeClient{table: &dbops.Table{Engine: "MergeTree()", Columns: first.table.Columns, OrderBy: "id"}}
		if immutable {
			third.table.PartitionBy = "id"
		}
		client := &tableNodeClient{nodes: []dbops.SchemaNode{{Host: "node1", Client: first}, {Host: "node2", Client: second}, {Host: "node3", Client: third}}}
		r := &Resource{client: client}
		_, state := plannedTable(t, r, "MergeTree()")
		plan := tfsdk.Plan(state)
		resp := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: state, Plan: plan}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if resp.Plan.Raw.Equal(state.Raw) {
			t.Fatal("node drift was hidden by the matching first node")
		}
		if immutable {
			marker := false
			for _, p := range resp.RequiresReplace {
				marker = marker || p.Equal(path.Root("create_statement"))
			}
			if !marker {
				t.Fatal("replacement has no changing attribute path")
			}
		}
		if immutable != (len(resp.RequiresReplace) > 0) {
			t.Fatalf("immutable=%v requires_replace=%v", immutable, resp.RequiresReplace)
		}
	}
}

func TestReplicatedAlterRereadsAcrossTopologyShards(t *testing.T) {
	engine := "ReplicatedMergeTree('/tables/t', '{replica}')"
	first := &tableNodeClient{table: &dbops.Table{Engine: engine, Columns: []dbops.Column{{Name: "id", Type: "UInt64"}}, OrderBy: "id"}}
	second := &tableNodeClient{table: &dbops.Table{Engine: engine, Columns: []dbops.Column{{Name: "id", Type: "UInt64"}}, OrderBy: "id"}}
	first.replicated = []*tableNodeClient{first, second}
	second.replicated = first.replicated
	client := &tableNodeClient{nodes: []dbops.SchemaNode{{Host: "node1", ShardNum: 1, Client: first}, {Host: "node2", ShardNum: 2, Client: second}}}
	r := &Resource{client: client}
	plan, _ := plannedTable(t, r, engine)
	_, diags := r.convergeTable(context.Background(), plan, true, false, false)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if first.alters != 1 || second.alters != 0 {
		t.Fatalf("shared Keeper metadata was altered twice: %d,%d", first.alters, second.alters)
	}
}

func (c *tableNodeClient) TableRows(context.Context, string, string) (uint64, error) {
	return c.rows, nil
}

func TestDataLossGuardChecksRemoteEngines(t *testing.T) {
	node := &tableNodeClient{table: &dbops.Table{Engine: "MergeTree()"}, rows: 1}
	r := &Resource{client: &tableNodeClient{nodes: []dbops.SchemaNode{{Host: "node", Client: node}}}}
	state := TableResourceModel{Database: types.StringValue("example"), Name: types.StringValue("t"), Engine: types.StringValue("Memory()")}
	if diags := r.guardDataLoss(context.Background(), state, "replace"); !diags.HasError() {
		t.Fatal("a non-MergeTree state hid rows on another node")
	}
}
