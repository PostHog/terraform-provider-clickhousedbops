package dbops

import (
	"context"
	"testing"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
)

type clusterRowsClient struct {
	rows    [][4]any
	queries []string
}

func (c *clusterRowsClient) Exec(context.Context, string, ...map[string]string) error { return nil }

func (c *clusterRowsClient) Select(_ context.Context, sql string, callback func(clickhouseclient.Row) error) error {
	c.queries = append(c.queries, sql)
	for _, values := range c.rows {
		row := clickhouseclient.Row{}
		for i, field := range []string{"host_name", "port", "shard_num", "replica_num"} {
			row.Set(field, values[i])
		}
		if err := callback(row); err != nil {
			return err
		}
	}
	return nil
}

func TestSchemaNodes(t *testing.T) {
	ctx := context.Background()

	t.Run("without fan-out the client is the only node", func(t *testing.T) {
		client, _ := NewClient(&clusterRowsClient{}, WithHost("entry"))
		nodes, err := client.SchemaNodes(ctx)
		if err != nil || len(nodes) != 1 || nodes[0].Host != "entry" || nodes[0].Client != client {
			t.Fatalf("SchemaNodes() = %#v, %v", nodes, err)
		}
	})

	t.Run("fan-out connects to every node in shard and replica order, once", func(t *testing.T) {
		entry := &clusterRowsClient{rows: [][4]any{
			{"s2r1", uint64(9000), uint64(2), uint64(1)},
			{"s1r2", uint64(9001), uint64(1), uint64(2)},
			{"s1r1", uint64(9000), uint64(1), uint64(1)},
		}}
		var dialed []string
		client, _ := NewClient(entry, WithHost("entry"), WithAdoptExisting(true), WithFanout("prod", func(host string, port uint16) (clickhouseclient.ClickhouseClient, error) {
			dialed = append(dialed, host)
			if host == "s1r2" && port != 9001 {
				t.Errorf("expected the port from system.clusters, got %d", port)
			}
			return &clusterRowsClient{}, nil
		}))

		for range 2 {
			nodes, err := client.SchemaNodes(ctx)
			if err != nil {
				t.Fatalf("SchemaNodes() error = %v", err)
			}
			var hosts []string
			for _, node := range nodes {
				hosts = append(hosts, node.Host)
				if node.Client == client || node.Client.FanoutCluster() != "" || !node.Client.AdoptExisting() {
					t.Errorf("node %s must have its own non-fan-out client", node.Host)
				}
			}
			if len(hosts) != 3 || hosts[0] != "s1r1" || hosts[1] != "s1r2" || hosts[2] != "s2r1" {
				t.Fatalf("unexpected node order: %v", hosts)
			}
		}
		if len(dialed) != 3 || len(entry.queries) != 1 {
			t.Fatalf("expected one discovery and three cached connections, got %d queries and %v", len(entry.queries), dialed)
		}
		if want := "SELECT `host_name`, toUInt64(port) AS `port`, toUInt64(shard_num) AS `shard_num`, toUInt64(replica_num) AS `replica_num` FROM `system`.`clusters` WHERE (`cluster` = 'prod');"; entry.queries[0] != want {
			t.Errorf("discovery query = %s, want %s", entry.queries[0], want)
		}
	})

	t.Run("a cluster without nodes is an error", func(t *testing.T) {
		client, _ := NewClient(&clusterRowsClient{}, WithFanout("missing", nil))
		if _, err := client.SchemaNodes(ctx); err == nil {
			t.Fatal("expected an error for a cluster with no nodes")
		}
	})
}
