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
	err error
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
		m := model{Node: types.ObjectNull(sr.Schema.Attributes["node"].GetType().(types.ObjectType).AttrTypes), ID: types.StringValue("example.t"), Database: types.StringValue("example"), Table: types.StringValue("t"), Format: types.StringValue("JSONEachRow"), Data: types.StringValue("invalid"), Checksum: types.StringUnknown()}
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
