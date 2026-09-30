package dbops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
)

// alterServer answers the queries of AlterTable from canned rows, keyed by the table they read.
type alterServer struct {
	execs     []string
	zkPath    string
	target    uint64
	replicas  map[string][]uint64 // metadata_version of each replica, one per poll
	polls     int
	mutations []map[string]any
}

func (s *alterServer) Exec(_ context.Context, sql string, _ ...map[string]string) error {
	s.execs = append(s.execs, sql)
	return nil
}

func (s *alterServer) Select(_ context.Context, sql string, callback func(clickhouseclient.Row) error, _ ...map[string]string) error {
	emit := func(values map[string]any) error {
		row := clickhouseclient.Row{}
		for field, value := range values {
			row.Set(field, value)
		}
		return callback(row)
	}
	switch {
	case strings.Contains(sql, "`system`.`one`"):
		return emit(map[string]any{"now": uint64(1000)})
	case strings.Contains(sql, "`system`.`replicas`"):
		if s.zkPath == "" {
			return nil
		}
		return emit(map[string]any{"zookeeper_path": s.zkPath})
	case strings.Contains(sql, "`name` = 'metadata'"):
		return emit(map[string]any{"version": s.target})
	case strings.Contains(sql, "`name` = 'metadata_version'"):
		for replica, versions := range s.replicas {
			version := versions[min(s.polls, len(versions)-1)]
			if err := emit(map[string]any{"path": s.zkPath + "/replicas/" + replica, "value": version}); err != nil {
				return err
			}
		}
		s.polls++
		return nil
	case strings.Contains(sql, "`system`.`zookeeper`"):
		for replica := range s.replicas {
			if err := emit(map[string]any{"name": replica}); err != nil {
				return err
			}
		}
		return nil
	case strings.Contains(sql, "`system`.`mutations`"):
		for _, mutation := range s.mutations {
			if err := emit(mutation); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func mutationRow(id string, created uint64, done bool, failReason string) map[string]any {
	isDone := uint64(0)
	if done {
		isDone = 1
	}
	return map[string]any{"mutation_id": id, "command": "MODIFY COLUMN `v` String", "created": created, "parts_to_do": uint64(4), "is_done": isDone, "latest_fail_reason": failReason}
}

func TestAlterTableDoesNotWaitForMutations(t *testing.T) {
	ctx := context.Background()
	alterPollInterval, mutationWatchTimeout, metadataWaitTimeout = time.Millisecond, 20*time.Millisecond, 50*time.Millisecond

	t.Run("waits for every replica's metadata, then reports the running mutation", func(t *testing.T) {
		server := &alterServer{
			zkPath:    "/clickhouse/tables/t",
			target:    3,
			replicas:  map[string][]uint64{"r1": {3}, "r2": {2, 2, 3}},
			mutations: []map[string]any{mutationRow("0000000001", 1000, false, ""), mutationRow("0000000000", 900, false, "")},
		}
		client, _ := NewClient(server)
		running, err := client.AlterTable(ctx, "db", "t", nil, []string{"MODIFY COLUMN `v` String"})
		if err != nil {
			t.Fatalf("AlterTable() error = %v", err)
		}
		if len(server.execs) != 1 || !strings.HasSuffix(server.execs[0], "SETTINGS alter_sync = 0;") {
			t.Errorf("expected one ALTER with alter_sync = 0, got %v", server.execs)
		}
		if server.polls < 3 {
			t.Errorf("expected to poll until r2 reached version 3, polled %d times", server.polls)
		}
		if len(running) != 1 || running[0].ID != "0000000001" {
			t.Errorf("expected only the mutation this ALTER started, got %+v", running)
		}
	})

	t.Run("a replica that never catches up is an error", func(t *testing.T) {
		server := &alterServer{zkPath: "/clickhouse/tables/t", target: 3, replicas: map[string][]uint64{"r1": {3}, "r2": {2}}}
		client, _ := NewClient(server)
		if _, err := client.AlterTable(ctx, "db", "t", nil, []string{"ADD COLUMN `w` UInt8"}); err == nil || !strings.Contains(err.Error(), "r2 (version 2)") {
			t.Fatalf("expected an error naming the lagging replica, got %v", err)
		}
	})

	t.Run("a failing mutation is an error that says how to stop it", func(t *testing.T) {
		server := &alterServer{mutations: []map[string]any{mutationRow("mutation_7.txt", 1000, false, "Cannot parse date")}}
		client, _ := NewClient(server)
		_, err := client.AlterTable(ctx, "db", "t", nil, []string{"MODIFY COLUMN `v` Date"})
		if err == nil || !strings.Contains(err.Error(), "Cannot parse date") || !strings.Contains(err.Error(), "mutation_id = 'mutation_7.txt'") {
			t.Fatalf("expected the failure and a KILL MUTATION hint, got %v", err)
		}
	})
}
