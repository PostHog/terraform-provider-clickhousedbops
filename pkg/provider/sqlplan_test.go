package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
)

func TestSQLPlanPrivateDataAndDigest(t *testing.T) {
	ctx := context.Background()
	server := Protocol6().(*sqlPlanServer)
	server.provider.enforceSQLPlan = true
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	typ := schemas.ResourceSchemas["clickhousedbops_table"].ValueType()
	attributes := map[string]tftypes.Value{}
	for name, attributeType := range typ.(tftypes.Object).AttributeTypes {
		attributes[name] = tftypes.NewValue(attributeType, nil)
	}
	attributes["sql_plan_digest"] = tftypes.NewValue(tftypes.String, "different digest")
	planned, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, attributes))
	if err != nil {
		t.Fatal(err)
	}
	null, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, nil))
	if err != nil {
		t.Fatal(err)
	}
	saved := savedSQLPlan{Version: 1, DeleteState: &null, Operations: []clickhouseclient.SQLOperation{{Target: "http://example.com:8123", SQL: "INSERT {data:String}", Parameters: map[string]string{"data": "invented fixture"}, DisplaySQL: "INSERT {data:String}"}}}
	encoded, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	decoded, ok := decodeSQLPlan(encoded)
	if !ok || len(decoded.Operations) != 1 || decoded.DeleteState == nil {
		t.Fatal("saved SQL program was lost across serialization")
	}
	report, err := sqlReport(decoded.Operations)
	if err != nil || strings.Contains(report, "invented fixture") || !strings.Contains(report, "[REDACTED]") {
		t.Fatalf("parameter redaction failed: %s: %v", report, err)
	}
	for _, private := range [][]byte{nil, []byte(`{"sql_plan_version":2}`), encoded} {
		resp, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: "clickhousedbops_table", PriorState: &null, PlannedState: &planned, PlannedPrivate: private})
		if err != nil || !hasErrors(resp.Diagnostics) {
			t.Fatalf("missing, unsupported or inconsistent SQL manifest was accepted: %v", err)
		}
	}
}
