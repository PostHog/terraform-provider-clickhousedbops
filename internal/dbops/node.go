package dbops

import (
	"fmt"

	"github.com/pingcap/errors"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
)

// NodeClientFactory opens a connection to one ClickHouse server. A port of 0 means the
// provider's port.
type NodeClientFactory func(host string, port uint16) (clickhouseclient.ClickhouseClient, error)

// Host is the server this client is connected to.
func (i *impl) Host() string {
	return i.host
}

func (i *impl) AdoptExisting() bool {
	return i.adoptExisting
}

func (i *impl) IgnoreColumnOrder() bool {
	return i.ignoreColumnOrder
}

func (i *impl) ManageDictionaryPasswords() bool {
	return i.manageDictPasswords
}

// ForNode returns a client connected to host, with this client's options. Connections are
// opened once per host and port, and shared by every resource that names the node.
func (i *impl) ForNode(host string, port uint16) (Client, error) {
	if i.nodeFactory == nil {
		return nil, errors.New("the provider is not configured to connect to other nodes")
	}
	key := fmt.Sprintf("%s:%d", host, port)

	i.nodesMu.Lock()
	defer i.nodesMu.Unlock()
	if client, ok := i.nodes[key]; ok {
		return client, nil
	}
	clickhouseClient, err := i.nodeFactory(host, port)
	if err != nil {
		return nil, errors.WithMessage(err, "error connecting to node "+key)
	}
	client := &impl{
		clickhouseClient:      clickhouseClient,
		readAfterWriteTimeout: i.readAfterWriteTimeout,
		host:                  host,
		adoptExisting:         i.adoptExisting,
		ignoreColumnOrder:     i.ignoreColumnOrder,
		manageDictPasswords:   i.manageDictPasswords,
	}
	if i.nodes == nil {
		i.nodes = map[string]Client{}
	}
	i.nodes[key] = client
	return client, nil
}
