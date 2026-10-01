package tablecontents

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

type checksumClient struct {
	dbops.Client
	err             error
	checksum        string
	exists          bool
	replicationPath string
	writes          *int
	nodes           []dbops.SchemaNode
}

func (c *checksumClient) DataChecksum(context.Context, string, string, string, string) (string, error) {
	return "", c.err
}

func TestChecksumPlanErrors(t *testing.T) {
	ctx := context.Background()
	for _, missing := range []bool{false, true} {
		err := errors.New("invalid declared JSON data")
		if missing {
			err = fmt.Errorf("creating table: %w", dbops.ErrTableNotFound)
		}
		r := &Resource{client: &checksumClient{err: err}}
		sr := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		plan := tfsdk.Plan{Schema: sr.Schema}
		m := model{ID: types.StringValue("example.t"), Database: types.StringValue("example"), Table: types.StringValue("t"), Format: types.StringValue("JSONEachRow"), Data: types.StringValue("invalid"), Checksum: types.StringUnknown()}
		if d := plan.Set(ctx, &m); d.HasError() {
			t.Fatal(d)
		}
		rr := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: plan}, &rr)
		if rr.Diagnostics.HasError() == missing {
			t.Fatalf("missing=%v: diagnostics=%v", missing, rr.Diagnostics)
		}
	}
}

func (c *checksumClient) SchemaNodes(context.Context) ([]dbops.SchemaNode, error) {
	return c.nodes, nil
}

func (c *checksumClient) TableContentsChecksum(context.Context, string, string) (string, bool, error) {
	return c.checksum, c.exists, c.err
}

func (c *checksumClient) ReplicationPath(context.Context, string, string) (string, error) {
	return c.replicationPath, nil
}

func (c *checksumClient) ReplaceTableContents(context.Context, string, string, string, string) error {
	*c.writes++
	return nil
}

func TestPartialTablePresence(t *testing.T) {
	present := &checksumClient{checksum: "1:abc", exists: true}
	missing := &checksumClient{}
	r := &Resource{client: &checksumClient{nodes: []dbops.SchemaNode{{Host: "present", Client: present}, {Host: "missing", Client: missing}}}}
	got, exists, diags := r.nodesChecksum(context.Background(), "example", "t")
	if diags.HasError() || !exists || got != nodesDiffer {
		t.Fatalf("partial presence was lost: %q %v %v", got, exists, diags)
	}
	present.exists = false
	_, exists, diags = r.nodesChecksum(context.Background(), "example", "t")
	if diags.HasError() || exists {
		t.Fatalf("absent table still has state: %v", diags)
	}
}

func TestContentsWriteUsesKeeperGroups(t *testing.T) {
	writes := 0
	nodes := []dbops.SchemaNode{}
	for index, path := range []string{"/shared", "/shared", "/other", "", ""} {
		nodes = append(nodes, dbops.SchemaNode{Host: fmt.Sprint(index), ShardNum: 1, Client: &checksumClient{replicationPath: path, writes: &writes, exists: true, checksum: ""}})
	}
	r := &Resource{client: &checksumClient{nodes: nodes}}
	m := model{Database: types.StringValue("example"), Table: types.StringValue("t"), Data: types.StringValue("{}"), Format: types.StringValue("JSONEachRow")}
	if diags := r.write(context.Background(), &m); diags.HasError() {
		t.Fatal(diags)
	}
	if writes != 4 {
		t.Fatalf("writes=%d, want one per Keeper group and every local table", writes)
	}
}
