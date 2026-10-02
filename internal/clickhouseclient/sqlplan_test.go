package clickhouseclient

import (
	"context"
	"encoding/json"
	"testing"
)

type writeCounter struct{ writes int }

func (c *writeCounter) Select(context.Context, string, func(Row) error, ...map[string]string) error {
	return nil
}

func (c *writeCounter) Exec(context.Context, string, ...map[string]string) error {
	c.writes++
	return nil
}

func TestSQLPlanBlocksUnreviewedWrites(t *testing.T) {
	base := &writeCounter{}
	client := WithSQLPlan(base, "http://node:8123")
	ctx, recording := RecordSQL(context.Background())
	if err := client.Exec(ctx, "ALTER TABLE events ADD COLUMN x UInt64"); err != nil {
		t.Fatal(err)
	}
	if err := client.Exec(WithMaskedQuery(ctx, "INSERT [REDACTED]"), "INSERT {data:String}", map[string]string{"data": "invented fixture"}); err != nil {
		t.Fatal(err)
	}
	if base.writes != 0 {
		t.Fatal("planning executed a write")
	}
	encoded, err := json.Marshal(recording.Operations)
	if err != nil {
		t.Fatal(err)
	}
	var operations []SQLOperation
	if err := json.Unmarshal(encoded, &operations); err != nil {
		t.Fatal(err)
	}
	if operations[1].DisplaySQL != "INSERT [REDACTED]" {
		t.Fatal("masked query was lost")
	}
	for _, tc := range []struct {
		name, target, sql string
		params            map[string]string
		missing           bool
	}{
		{name: "missing plan", target: "http://node:8123", sql: operations[0].SQL, missing: true},
		{name: "SQL", target: "http://node:8123", sql: "DROP TABLE events"},
		{name: "target", target: "http://other:8123", sql: operations[0].SQL},
		{name: "parameter", target: "http://node:8123", sql: operations[1].SQL, params: map[string]string{"data": "different"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := EnforceSQL(context.Background(), operations)
			if tc.missing {
				ctx = context.Background()
			}
			if err := WithSQLPlan(base, tc.target).Exec(ctx, tc.sql, tc.params); err == nil {
				t.Fatal("unreviewed write was allowed")
			}
			if base.writes != 0 {
				t.Fatal("blocked write reached ClickHouse")
			}
		})
	}
	ctx = EnforceSQL(context.Background(), operations)
	// Earlier operations may be skipped when replication has already satisfied them.
	if err := client.Exec(ctx, operations[1].SQL, operations[1].Parameters); err != nil {
		t.Fatal(err)
	}
	if err := client.Exec(ctx, operations[0].SQL); err == nil {
		t.Fatal("out-of-order write was allowed")
	}
	if err := client.Exec(ctx, operations[1].SQL, operations[1].Parameters); err == nil {
		t.Fatal("repeated write was allowed")
	}
	if base.writes != 1 {
		t.Fatalf("expected exactly one approved write, got %d", base.writes)
	}
}
