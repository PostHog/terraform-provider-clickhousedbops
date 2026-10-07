package table

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

type tableNodeClient struct {
	dbops.Client
	table  *dbops.Table
	alters [][]string
	waits  int
	rows   uint64
}

func (c *tableNodeClient) IgnoreColumnOrder() bool { return true }
func (c *tableNodeClient) Host() string            { return "node" }
func (c *tableNodeClient) WaitForReplicaMetadata(context.Context, string, string) error {
	c.waits++
	return nil
}
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
	c.alters = append(c.alters, actions)
	for _, action := range actions {
		if strings.HasPrefix(action, "ADD COLUMN") {
			c.table.Columns = append(c.table.Columns, dbops.Column{Name: "added", Type: "UInt64"})
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
	node := types.ObjectNull(sr.Schema.Attributes["node"].GetType().(types.ObjectType).AttrTypes)
	m := TableResourceModel{Database: types.StringValue("example"), Name: types.StringValue("t"), Engine: types.StringValue(engine), Columns: columns, OrderBy: types.StringValue("id"), Node: node, CreateStatement: types.StringValue("CREATE TABLE example.t"), ForceDestroy: types.BoolValue(true)}
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

func TestPlanShowsDriftAndReplacement(t *testing.T) {
	for _, immutable := range []bool{false, true} {
		node := &tableNodeClient{table: &dbops.Table{Engine: "MergeTree()", Columns: []dbops.Column{{Name: "id", Type: "UInt64"}}, OrderBy: "id"}}
		if immutable {
			node.table.Columns = append(node.table.Columns, dbops.Column{Name: "added", Type: "UInt64"})
			node.table.PartitionBy = "id"
		}
		r := &Resource{client: node}
		_, state := plannedTable(t, r, "MergeTree()")
		plan := tfsdk.Plan(state)
		resp := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: state, Plan: plan}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if resp.Plan.Raw.Equal(state.Raw) {
			t.Fatal("the node's drift does not show in the plan")
		}
		if immutable != (len(resp.RequiresReplace) > 0) {
			t.Fatalf("immutable=%v requires_replace=%v", immutable, resp.RequiresReplace)
		}
	}
}

func TestReplicaRoles(t *testing.T) {
	engine := "ReplicatedMergeTree('/tables/t', '{replica}')"
	for _, tc := range []struct {
		name        string
		role        string
		hasColumn   bool
		wantAlters  int
		wantWaits   int
		wantFailure bool
	}{
		{name: "leader runs the replicated ALTER", role: replicaRoleLeader, wantAlters: 1},
		{name: "no role runs it as a leader does", wantAlters: 1},
		{name: "follower after its leader runs nothing", role: replicaRoleFollower, hasColumn: true, wantWaits: 2},
		{name: "follower before its leader fails without altering", role: replicaRoleFollower, wantWaits: 1, wantFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &tableNodeClient{table: &dbops.Table{Engine: engine, Columns: []dbops.Column{{Name: "id", Type: "UInt64"}}, OrderBy: "id"}}
			if tc.hasColumn {
				node.table.Columns = append(node.table.Columns, dbops.Column{Name: "added", Type: "UInt64"})
			}
			r := &Resource{client: node}
			plan, _ := plannedTable(t, r, engine)
			plan.ReplicaRole = types.StringNull()
			if tc.role != "" {
				plan.ReplicaRole = types.StringValue(tc.role)
			}
			_, diags := r.convergeTable(context.Background(), plan, true, false, false)
			if diags.HasError() != tc.wantFailure {
				t.Fatalf("failure=%v, want %v: %v", diags.HasError(), tc.wantFailure, diags)
			}
			if len(node.alters) != tc.wantAlters || node.waits != tc.wantWaits {
				t.Fatalf("alters=%d waits=%d, want %d and %d", len(node.alters), node.waits, tc.wantAlters, tc.wantWaits)
			}
		})
	}
}

func (c *tableNodeClient) TableRows(context.Context, string, string) (uint64, error) {
	return c.rows, nil
}

func TestDataLossGuardChecksRemoteEngines(t *testing.T) {
	node := &tableNodeClient{table: &dbops.Table{Engine: "MergeTree()"}, rows: 1}
	r := &Resource{client: node}
	state := TableResourceModel{Database: types.StringValue("example"), Name: types.StringValue("t"), Engine: types.StringValue("Memory()")}
	if diags := r.guardDataLoss(context.Background(), state, "replace"); !diags.HasError() {
		t.Fatal("a non-MergeTree state hid the rows of the node's MergeTree table")
	}
}
