package schemahelpers

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

// NodeConverger describes how one kind of schema object is read, created and changed in
// place on a single node.
type NodeConverger[T any] struct {
	// Kind and QualifiedName identify the object in error messages.
	Kind          string
	QualifiedName string
	Get           func(ctx context.Context, client dbops.Client) (*T, error)
	// Create creates the object on a node that does not have it. reference is the object
	// as another node of the same shard holds it, or nil when no node of the shard has it.
	Create func(ctx context.Context, client dbops.Client, reference *T) error
	// Reconcile changes an existing object in place. firstInShard is true for the first
	// node of each shard that already has the object.
	Reconcile func(ctx context.Context, node dbops.SchemaNode, firstInShard bool, existing *T) error
}

// PlanNodes rejects cluster_name together with fanout_cluster and sets the planned nodes
// attribute to the live node list.
func PlanNodes(ctx context.Context, client dbops.Client, clusterName types.String, plan *tfsdk.Plan) diag.Diagnostics {
	var diags diag.Diagnostics

	if client.FanoutCluster() != "" && !clusterName.IsNull() && !clusterName.IsUnknown() {
		diags.AddAttributeError(
			path.Root("cluster_name"),
			"Invalid Attribute Combination",
			"cluster_name cannot be set when the provider sets fanout_cluster. With fanout_cluster the provider runs the DDL on every node without ON CLUSTER.",
		)
		return diags
	}

	nodes, err := client.SchemaNodes(ctx)
	if err != nil {
		diags.AddError("Error listing cluster nodes", fmt.Sprintf("%+v\n", err))
		return diags
	}

	hosts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		// The host is empty while the provider configuration is not known yet.
		if node.Host == "" {
			return diags
		}
		hosts = append(hosts, node.Host)
	}

	diags.Append(plan.SetAttribute(ctx, path.Root("nodes"), hosts)...)
	return diags
}

// PlanNodeStates compares every node with the desired managed definition. Missing or
// differing nodes must keep create_statement unknown so that apply converges the cluster.
func PlanNodeStates[M any, T any](
	ctx context.Context, client dbops.Client, desired M,
	get func(context.Context, dbops.Client) (*T, error),
	sync func(context.Context, *M, *T) diag.Diagnostics,
) ([]M, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	nodes, err := client.SchemaNodes(ctx)
	if err != nil {
		diags.AddError("Error listing cluster nodes", err.Error())
		return nil, false, diags
	}
	var states []M
	converged := true
	for _, node := range nodes {
		object, err := get(ctx, node.Client)
		if err != nil {
			diags.AddError(fmt.Sprintf("Error reading from node %q", node.Host), err.Error())
			return nil, false, diags
		}
		if object == nil {
			converged = false
			continue
		}
		candidate := desired
		diags.Append(sync(ctx, &candidate, object)...)
		if diags.HasError() {
			return nil, false, diags
		}
		converged = converged && reflect.DeepEqual(candidate, desired)
		states = append(states, candidate)
	}
	return states, converged, diags
}

// ReadNodes reads the object from every node and derives the new state with sync. The state
// comes from the first node that has the object, or from the first node whose definition
// differs from the current state, so that drift on any node is visible. It returns a nil
// state when no node has the object.
func ReadNodes[M any, T any](
	ctx context.Context,
	client dbops.Client,
	state M,
	get func(ctx context.Context, client dbops.Client) (*T, error),
	sync func(ctx context.Context, state *M, object *T) diag.Diagnostics,
) (*M, *T, types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	noNodes := types.ListNull(types.StringType)

	nodes, err := client.SchemaNodes(ctx)
	if err != nil {
		diags.AddError("Error listing cluster nodes", fmt.Sprintf("%+v\n", err))
		return nil, nil, noNodes, diags
	}

	var (
		chosen       *M
		chosenObject *T
		differs      bool
		hosts        []string
	)
	for _, node := range nodes {
		object, err := get(ctx, node.Client)
		if err != nil {
			diags.AddError(fmt.Sprintf("Error reading from node %q", node.Host), fmt.Sprintf("%+v\n", err))
			return nil, nil, noNodes, diags
		}
		if object == nil {
			continue
		}
		hosts = append(hosts, node.Host)
		if differs {
			continue
		}

		candidate := state
		diags.Append(sync(ctx, &candidate, object)...)
		if diags.HasError() {
			return nil, nil, noNodes, diags
		}
		changed := !reflect.DeepEqual(candidate, state)
		if chosen == nil || changed {
			chosen, chosenObject, differs = &candidate, object, changed
		}
	}

	return chosen, chosenObject, hostList(hosts), diags
}

// ConvergeNodes makes every node hold the desired object: it changes existing objects in
// place, then creates the object where it is missing. When adopt is false, an object that
// already exists on any node is an error. It returns the object as the first node reports it.
func ConvergeNodes[T any](ctx context.Context, client dbops.Client, adopt bool, converger NodeConverger[T]) (*T, types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	noNodes := types.ListNull(types.StringType)

	nodes, err := client.SchemaNodes(ctx)
	if err != nil {
		diags.AddError("Error listing cluster nodes", fmt.Sprintf("%+v\n", err))
		return nil, noNodes, diags
	}

	existing := make([]*T, len(nodes))
	var existingHosts, hosts []string
	for index, node := range nodes {
		existing[index], err = converger.Get(ctx, node.Client)
		if err != nil {
			diags.AddError(fmt.Sprintf("Error reading %s from node %q", converger.Kind, node.Host), fmt.Sprintf("%+v\n", err))
			return nil, noNodes, diags
		}
		if existing[index] != nil {
			existingHosts = append(existingHosts, node.Host)
		}
		hosts = append(hosts, node.Host)
	}

	if !adopt && len(existingHosts) > 0 {
		diags.AddError(
			fmt.Sprintf("%s already exists", titleCase(converger.Kind)),
			fmt.Sprintf("The %s %s already exists on node(s) %s. Import it, or set adopt_existing = true in the provider configuration.",
				converger.Kind, converger.QualifiedName, strings.Join(existingHosts, ", ")),
		)
		return nil, noNodes, diags
	}

	// Existing objects are changed first: a replicated table must reach the desired
	// definition in Keeper before a new replica is created with that definition.
	firstInShard := make(map[uint64]dbops.SchemaNode)
	for index, node := range nodes {
		if existing[index] == nil {
			continue
		}
		// Earlier ALTERs may have changed this replica through Keeper, across topology shards.
		current, err := converger.Get(ctx, node.Client)
		if err != nil || current == nil {
			diags.AddError(fmt.Sprintf("Error rereading %s on node %q", converger.Kind, node.Host), fmt.Sprintf("Object disappeared or could not be read: %v", err))
			return nil, noNodes, diags
		}
		_, seen := firstInShard[node.ShardNum]
		if err := converger.Reconcile(ctx, node, !seen, current); err != nil {
			diags.AddError(fmt.Sprintf("Error updating %s on node %q", converger.Kind, node.Host), err.Error())
			return nil, noNodes, diags
		}
		if !seen {
			firstInShard[node.ShardNum] = node
		}
	}
	references := make(map[uint64]*T)
	for index, node := range nodes {
		if existing[index] != nil {
			continue
		}
		if referenceNode, ok := firstInShard[node.ShardNum]; ok && references[node.ShardNum] == nil {
			references[node.ShardNum], err = converger.Get(ctx, referenceNode.Client)
			if err != nil {
				diags.AddError(fmt.Sprintf("Error reading %s from node %q", converger.Kind, referenceNode.Host), fmt.Sprintf("%+v\n", err))
				return nil, noNodes, diags
			}
		}
		if err := converger.Create(ctx, node.Client, references[node.ShardNum]); err != nil {
			diags.AddError(fmt.Sprintf("Error creating %s on node %q", converger.Kind, node.Host), err.Error())
			return nil, noNodes, diags
		}
	}

	object, err := converger.Get(ctx, nodes[0].Client)
	if err != nil {
		diags.AddError(fmt.Sprintf("Error reading %s from node %q", converger.Kind, nodes[0].Host), fmt.Sprintf("%+v\n", err))
		return nil, noNodes, diags
	}
	if object == nil {
		diags.AddError(fmt.Sprintf("Error reading %s", converger.Kind), fmt.Sprintf("The %s %s was not found on node %q after it was applied.", converger.Kind, converger.QualifiedName, nodes[0].Host))
		return nil, noNodes, diags
	}

	return object, hostList(hosts), diags
}

// DeleteNodes drops the object on every node.
func DeleteNodes(ctx context.Context, client dbops.Client, drop func(ctx context.Context, client dbops.Client) error) diag.Diagnostics {
	var diags diag.Diagnostics

	nodes, err := client.SchemaNodes(ctx)
	if err != nil {
		diags.AddError("Error listing cluster nodes", fmt.Sprintf("%+v\n", err))
		return diags
	}
	for _, node := range nodes {
		if err := drop(ctx, node.Client); err != nil {
			diags.AddError(fmt.Sprintf("Error deleting from node %q", node.Host), fmt.Sprintf("%+v\n", err))
		}
	}

	return diags
}

func hostList(hosts []string) types.List {
	values := make([]types.String, 0, len(hosts))
	for _, host := range hosts {
		values = append(values, types.StringValue(host))
	}
	list, _ := types.ListValueFrom(context.Background(), types.StringType, values)
	return list
}

// EquivalentString pairs a planned SQL attribute with its value in state.
type EquivalentString struct {
	Attribute string
	Planned   types.String
	State     types.String
	// Equal compares the two texts; nil compares them as SQL, ignoring whitespace.
	Equal func(planned string, state string) bool
}

// KeepEquivalentStrings makes every planned attribute whose text means the same as the text
// in state keep the state's text, so that formatting differences do not show as changes. When
// all of them are equivalent and the node list is unchanged, nothing will run, and the create
// statement keeps its value too.
func KeepEquivalentStrings(ctx context.Context, plan *tfsdk.Plan, stateNodes types.List, stateCreateStatement types.String, attributes []EquivalentString) diag.Diagnostics {
	var diags diag.Diagnostics

	unchanged := true
	for _, attribute := range attributes {
		equal := attribute.Equal
		if equal == nil {
			equal = SQLEqual
		}
		if attribute.Planned.IsUnknown() || !equal(attribute.Planned.ValueString(), attribute.State.ValueString()) {
			unchanged = false
			continue
		}
		diags.Append(plan.SetAttribute(ctx, path.Root(attribute.Attribute), attribute.State)...)
	}

	var plannedNodes types.List
	diags.Append(plan.GetAttribute(ctx, path.Root("nodes"), &plannedNodes)...)
	if unchanged && plannedNodes.Equal(stateNodes) {
		diags.Append(plan.SetAttribute(ctx, path.Root("create_statement"), stateCreateStatement)...)
	}

	return diags
}
