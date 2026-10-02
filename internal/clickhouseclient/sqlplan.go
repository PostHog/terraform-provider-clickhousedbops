package clickhouseclient

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sync"
)

// SQLOperation binds a write to its connection and parameter values, not just its SQL text.
type SQLOperation struct {
	Target     string            `json:"target"`
	SQL        string            `json:"sql"`
	Parameters map[string]string `json:"parameters,omitempty"`
	DisplaySQL string            `json:"display_sql,omitempty"`
}

type SQLPlan struct {
	Operations []SQLOperation `json:"operations"`
	mu         sync.Mutex
	recording  bool
	positions  map[string]int
	objects    map[previewKey]any
}

type sqlPlanKey struct{}

type previewKey struct{ host, kind, database, name string }

func RecordSQL(ctx context.Context) (context.Context, *SQLPlan) {
	plan := &SQLPlan{recording: true, objects: make(map[previewKey]any)}
	return context.WithValue(ctx, sqlPlanKey{}, plan), plan
}

func EnforceSQL(ctx context.Context, operations []SQLOperation) context.Context {
	return context.WithValue(ctx, sqlPlanKey{}, &SQLPlan{Operations: operations, positions: make(map[string]int)})
}

func IsRecordingSQL(ctx context.Context) bool {
	plan, ok := ctx.Value(sqlPlanKey{}).(*SQLPlan)
	return ok && plan.recording
}

// PreviewObject keeps dry-run reads consistent with preceding creates and drops.
func PreviewObject[T any](ctx context.Context, host, kind, database, name string) (*T, bool) {
	plan, ok := ctx.Value(sqlPlanKey{}).(*SQLPlan)
	if !ok || !plan.recording {
		return nil, false
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	value, found := plan.objects[previewKey{host, kind, database, name}]
	if !found {
		return nil, false
	}
	return value.(*T), true
}

func SetPreviewObject(ctx context.Context, host, kind, database, name string, object any) {
	plan, ok := ctx.Value(sqlPlanKey{}).(*SQLPlan)
	if !ok || !plan.recording {
		return
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.objects[previewKey{host, kind, database, name}] = object
}

func SQLPlanDigest(operations []SQLOperation) (string, error) {
	data, err := json.Marshal(operations)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

type reviewedClient struct {
	ClickhouseClient
	target string
}

func WithSQLPlan(client ClickhouseClient, target string) ClickhouseClient {
	return &reviewedClient{ClickhouseClient: client, target: target}
}

func (c *reviewedClient) Exec(ctx context.Context, sql string, params ...map[string]string) error {
	plan, ok := ctx.Value(sqlPlanKey{}).(*SQLPlan)
	if !ok {
		return fmt.Errorf("SQL execution plan is missing for %s; create a new plan with enforce_sql_plan enabled", c.target)
	}
	var parameters map[string]string
	if len(params) > 0 && len(params[0]) > 0 {
		parameters = maps.Clone(params[0])
	}
	// ponytail: serialize resource writes; use per-target locks if node convergence becomes concurrent.
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.recording {
		plan.Operations = append(plan.Operations, SQLOperation{Target: c.target, SQL: sql, Parameters: parameters, DisplaySQL: loggableQuery(ctx, sql)})
		return nil
	}
	// Replication or an already satisfied operation may remove writes, but never add them.
	for index := plan.positions[c.target]; index < len(plan.Operations); index++ {
		operation := plan.Operations[index]
		if operation.Target == c.target && operation.SQL == sql && reflect.DeepEqual(operation.Parameters, parameters) {
			plan.positions[c.target] = index + 1
			return c.ClickhouseClient.Exec(ctx, sql, params...)
		}
	}
	return fmt.Errorf("unreviewed SQL blocked on %s; the statement, parameters, target or execution order changed; create a new plan", c.target)
}
