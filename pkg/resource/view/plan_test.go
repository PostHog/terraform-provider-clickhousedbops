package view

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

type nodeClient struct {
	dbops.Client
	nodes []dbops.SchemaNode
	query string
}

func (c *nodeClient) SchemaNodes(context.Context) ([]dbops.SchemaNode, error) { return c.nodes, nil }
func (c *nodeClient) FanoutCluster() string                                   { return "example" }
func (c *nodeClient) GetView(context.Context, string, string, *string) (*dbops.View, error) {
	return &dbops.View{Database: "example", Name: "v", Query: c.query, CreateStatement: "CREATE VIEW example.v AS " + c.query}, nil
}

func (c *nodeClient) ReplaceView(_ context.Context, view dbops.View, _ *string) (*dbops.View, error) {
	c.query = view.Query
	return c.GetView(context.Background(), view.Database, view.Name, nil)
}

func TestPartialRolloutPlansAndConverges(t *testing.T) {
	ctx := context.Background()
	first := &nodeClient{query: "SELECT 2"}
	second := &nodeClient{query: "SELECT 1"}
	client := &nodeClient{nodes: []dbops.SchemaNode{{Host: "node1", ShardNum: 1, ReplicaNum: 1, Client: first}, {Host: "node2", ShardNum: 1, ReplicaNum: 2, Client: second}}}
	r := &Resource{client: client}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	prior := tfsdk.State{Schema: sr.Schema}
	columns, d := schemahelpers.ColumnSignaturesValue(ctx, nil)
	if d.HasError() {
		t.Fatal(d)
	}
	nodes, d := types.ListValueFrom(ctx, types.StringType, []string{"node1", "node2"})
	if d.HasError() {
		t.Fatal(d)
	}
	model := ViewResourceModel{ClusterName: types.StringNull(), Database: types.StringValue("example"), Name: types.StringValue("v"), ID: types.StringValue("example.v"), QualifiedName: types.StringValue("`example`.`v`"), Nodes: nodes, Columns: columns, Query: types.StringValue("SELECT 1"), CreateStatement: types.StringValue("CREATE VIEW example.v AS SELECT 1")}
	if d := prior.Set(ctx, &model); d.HasError() {
		t.Fatal(d)
	}
	rr := resource.ReadResponse{State: prior}
	r.Read(ctx, resource.ReadRequest{State: prior}, &rr)
	if rr.Diagnostics.HasError() {
		t.Fatal(rr.Diagnostics)
	}
	planned := tfsdk.Plan{Schema: sr.Schema, Raw: rr.State.Raw}
	pr := resource.ModifyPlanResponse{Plan: planned}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: rr.State, Plan: planned}, &pr)
	if pr.Diagnostics.HasError() {
		t.Fatal(pr.Diagnostics)
	}
	if pr.Plan.Raw.Equal(rr.State.Raw) {
		t.Fatal("plan is empty although node2 still has SELECT 1 and the desired query is SELECT 2")
	}
	update := resource.UpdateResponse{State: rr.State}
	r.Update(ctx, resource.UpdateRequest{Plan: pr.Plan}, &update)
	if update.Diagnostics.HasError() {
		t.Fatal(update.Diagnostics)
	}
	if first.query != "SELECT 2" || second.query != "SELECT 2" {
		t.Fatal("apply did not converge every node")
	}
	next := tfsdk.Plan{Schema: sr.Schema, Raw: update.State.Raw}
	response := resource.ModifyPlanResponse{Plan: next}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: update.State, Plan: next}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	if !response.Plan.Raw.Equal(update.State.Raw) {
		t.Fatal("converged cluster still plans an update")
	}
}
