package dbops

import (
	"context"
	"sort"

	"github.com/pingcap/errors"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
)

// SchemaNode is one ClickHouse server that schema objects are applied to.
type SchemaNode struct {
	Host       string
	ShardNum   uint64
	ReplicaNum uint64
	Client     Client
}

// NodeClientFactory opens a connection to one cluster node. address is the IP address and port
// the node's native protocol port, both as reported by system.clusters.
type NodeClientFactory func(address string, port uint16) (clickhouseclient.ClickhouseClient, error)

func (i *impl) FanoutCluster() string {
	return i.fanoutCluster
}

func (i *impl) AdoptExisting() bool {
	return i.adoptExisting
}

func (i *impl) IgnoreColumnOrder() bool {
	return i.ignoreColumnOrder
}

func (i *impl) SchemaNodes(ctx context.Context) ([]SchemaNode, error) {
	if i.fanoutCluster == "" {
		return []SchemaNode{{Host: i.host, ShardNum: 1, ReplicaNum: 1, Client: i}}, nil
	}

	i.nodesMu.Lock()
	defer i.nodesMu.Unlock()
	if i.nodes != nil {
		return i.nodes, nil
	}

	sql, err := querybuilder.NewSelect(
		[]querybuilder.Field{
			querybuilder.NewField("host_name"),
			querybuilder.NewField("host_address"),
			querybuilder.NewRawField("toUInt64(port)", "port"),
			querybuilder.NewRawField("toUInt64(shard_num)", "shard_num"),
			querybuilder.NewRawField("toUInt64(replica_num)", "replica_num"),
		},
		"system.clusters",
	).Where(querybuilder.WhereEquals("cluster", i.fanoutCluster)).Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	type discoveredNode struct {
		SchemaNode
		address string
		port    uint64
	}
	discovered := make([]discoveredNode, 0)
	err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		node := discoveredNode{}
		var rowErr error
		if node.Host, rowErr = data.GetString("host_name"); rowErr != nil {
			return rowErr
		}
		if node.address, rowErr = data.GetString("host_address"); rowErr != nil {
			return rowErr
		}
		if node.port, rowErr = data.GetUInt64("port"); rowErr != nil {
			return rowErr
		}
		if node.ShardNum, rowErr = data.GetUInt64("shard_num"); rowErr != nil {
			return rowErr
		}
		if node.ReplicaNum, rowErr = data.GetUInt64("replica_num"); rowErr != nil {
			return rowErr
		}
		discovered = append(discovered, node)
		return nil
	})
	if err != nil {
		return nil, errors.WithMessage(err, "error running query")
	}
	if len(discovered) == 0 {
		return nil, errors.Errorf("fanout_cluster %q has no nodes in system.clusters", i.fanoutCluster)
	}

	sort.SliceStable(discovered, func(a, b int) bool {
		if discovered[a].ShardNum != discovered[b].ShardNum {
			return discovered[a].ShardNum < discovered[b].ShardNum
		}
		return discovered[a].ReplicaNum < discovered[b].ReplicaNum
	})

	nodes := make([]SchemaNode, 0, len(discovered))
	for _, node := range discovered {
		// The server resolves host_name to host_address when it loads the cluster, so connecting
		// to the address does not depend on the caller resolving the node's name.
		if node.address == "" {
			return nil, errors.Errorf("node %q of fanout_cluster %q has no host_address in system.clusters: the server could not resolve it", node.Host, i.fanoutCluster)
		}
		clickhouseClient, err := i.nodeFactory(node.address, uint16(node.port)) //nolint:gosec
		if err != nil {
			return nil, errors.WithMessage(err, "error connecting to node "+node.Host)
		}
		node.Client = &impl{
			clickhouseClient:      clickhouseClient,
			readAfterWriteTimeout: i.readAfterWriteTimeout,
			host:                  node.Host,
			adoptExisting:         i.adoptExisting,
			ignoreColumnOrder:     i.ignoreColumnOrder,
		}
		nodes = append(nodes, node.SchemaNode)
	}

	i.nodes = nodes
	return nodes, nil
}
