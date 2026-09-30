package schemahelpers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

// fakeNodeClient is one node of a fake cluster. definition is the object the node holds,
// or an empty string when the node does not have it.
type fakeNodeClient struct {
	dbops.Client
	nodes      []dbops.SchemaNode
	definition string
}

func (c *fakeNodeClient) SchemaNodes(context.Context) ([]dbops.SchemaNode, error) {
	return c.nodes, nil
}

// fakeCluster builds a cluster from "shard:definition" entries in discovery order.
func fakeCluster(entries ...string) (*fakeNodeClient, []*fakeNodeClient) {
	entry := &fakeNodeClient{}
	clients := make([]*fakeNodeClient, 0, len(entries))
	for index, spec := range entries {
		shard, definition, _ := strings.Cut(spec, ":")
		client := &fakeNodeClient{definition: definition}
		clients = append(clients, client)
		entry.nodes = append(entry.nodes, dbops.SchemaNode{
			Host:       "node" + string(rune('1'+index)),
			ShardNum:   uint64(shard[0] - '0'),
			ReplicaNum: uint64(index + 1),
			Client:     client,
		})
	}
	return entry, clients
}

func fakeGet(_ context.Context, client dbops.Client) (*string, error) {
	if node := client.(*fakeNodeClient); node.definition != "" {
		return &node.definition, nil
	}
	return nil, nil
}

func hosts(t *testing.T, list types.List) string {
	t.Helper()
	var values []string
	if diags := list.ElementsAs(context.Background(), &values, false); diags.HasError() {
		t.Fatalf("invalid nodes list: %v", diags)
	}
	return strings.Join(values, ",")
}

func TestReadNodes(t *testing.T) {
	sync := func(_ context.Context, state *string, object *string) diag.Diagnostics {
		*state = *object
		return nil
	}

	tests := []struct {
		name      string
		cluster   []string
		state     string
		wantState string
		wantHosts string
		wantGone  bool
	}{
		{name: "all nodes match the state", cluster: []string{"1:v1", "1:v1"}, state: "v1", wantState: "v1", wantHosts: "node1,node2"},
		{name: "state comes from the first node that differs", cluster: []string{"1:v1", "1:v2", "2:v3"}, state: "v1", wantState: "v2", wantHosts: "node1,node2,node3"},
		{name: "a node without the object is left out of nodes", cluster: []string{"1:", "1:v1"}, state: "v1", wantState: "v1", wantHosts: "node2"},
		{name: "missing everywhere removes the resource", cluster: []string{"1:", "1:"}, state: "v1", wantGone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := fakeCluster(tt.cluster...)
			state, _, nodes, diags := ReadNodes(context.Background(), client, tt.state, fakeGet, sync)
			if diags.HasError() {
				t.Fatalf("ReadNodes() diagnostics = %v", diags)
			}
			if tt.wantGone {
				if state != nil {
					t.Fatalf("expected no state, got %q", *state)
				}
				return
			}
			if state == nil || *state != tt.wantState || hosts(t, nodes) != tt.wantHosts {
				t.Fatalf("ReadNodes() state = %v, nodes = %s, want %q and %s", state, hosts(t, nodes), tt.wantState, tt.wantHosts)
			}
		})
	}
}

func TestConvergeNodes(t *testing.T) {
	tests := []struct {
		name    string
		cluster []string
		adopt   bool
		// wantLog lists the actions in order: "alter node (first)" for the first existing node
		// of a shard, "alter node" for the others, "create node from <reference>".
		wantLog   []string
		wantError string
	}{
		{
			name:    "creates the object on every node of an empty cluster",
			cluster: []string{"1:", "1:"},
			wantLog: []string{"create node1 from <nil>", "create node2 from <nil>"},
		},
		{
			name:      "an existing object is an error without adoption",
			cluster:   []string{"1:", "1:old"},
			wantError: "already exists on node(s) node2",
		},
		{
			name:    "existing nodes are changed before missing nodes are created from their shard",
			cluster: []string{"1:", "1:old", "1:old", "2:"},
			adopt:   true,
			wantLog: []string{"alter node2 (first)", "alter node3", "create node1 from new", "create node4 from <nil>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := fakeCluster(tt.cluster...)
			var log []string
			hostOf := func(target dbops.Client) string {
				for _, node := range client.nodes {
					if node.Client == target {
						return node.Host
					}
				}
				return "?"
			}

			_, nodes, diags := ConvergeNodes(context.Background(), client, tt.adopt, NodeConverger[string]{
				Kind:          "table",
				QualifiedName: "db.t",
				Get:           fakeGet,
				Create: func(_ context.Context, target dbops.Client, reference *string) error {
					from := "<nil>"
					if reference != nil {
						from = *reference
					}
					log = append(log, "create "+hostOf(target)+" from "+from)
					target.(*fakeNodeClient).definition = "new"
					return nil
				},
				Reconcile: func(_ context.Context, node dbops.SchemaNode, firstInShard bool, existing *string) error {
					if *existing != "old" {
						return errors.New("unexpected existing definition")
					}
					entry := "alter " + node.Host
					if firstInShard {
						entry += " (first)"
					}
					log = append(log, entry)
					node.Client.(*fakeNodeClient).definition = "new"
					return nil
				},
			})

			if tt.wantError != "" {
				if !diags.HasError() || !strings.Contains(diags[0].Detail(), tt.wantError) || len(log) != 0 {
					t.Fatalf("expected error %q before any change, got %v and %v", tt.wantError, diags, log)
				}
				return
			}
			if diags.HasError() {
				t.Fatalf("ConvergeNodes() diagnostics = %v", diags)
			}
			if strings.Join(log, "; ") != strings.Join(tt.wantLog, "; ") {
				t.Errorf("actions = %v, want %v", log, tt.wantLog)
			}
			if got := hosts(t, nodes); strings.Count(got, ",")+1 != len(tt.cluster) {
				t.Errorf("expected every node in nodes, got %s", got)
			}
		})
	}
}
