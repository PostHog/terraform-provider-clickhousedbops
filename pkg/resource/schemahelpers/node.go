package schemahelpers

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

// NodeModel is the server a schema object lives on.
type NodeModel struct {
	Name types.String `tfsdk:"name"`
	Host types.String `tfsdk:"host"`
	Port types.Int32  `tfsdk:"port"`
}

func nodeAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{"name": types.StringType, "host": types.StringType, "port": types.Int32Type}
}

// NodeAttribute declares the server a schema object lives on. Without it, the object lives on
// the provider's host.
func NodeAttribute(objectType string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:    true,
		Description: fmt.Sprintf("The ClickHouse server the %s lives on. If omitted, the %s lives on the provider's host. The connection uses the provider's protocol, credentials and TLS settings.", objectType, objectType),
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Stable name of the server. A different name is a different server, so changing it replaces the object.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"host": schema.StringAttribute{
				Required:    true,
				Description: "Address to connect to. Changing it reconnects without replacing the object.",
			},
			"port": schema.Int32Attribute{
				Optional:    true,
				Description: "Port to connect to. Defaults to the provider's port.",
			},
		},
	}
}

// NodeClient returns a client for the node that get reads from a plan or a state, or client
// when the object names no node or the provider is not configured yet.
func NodeClient(ctx context.Context, client dbops.Client, get func(context.Context, path.Path, interface{}) diag.Diagnostics) (dbops.Client, diag.Diagnostics) {
	if client == nil {
		return nil, nil
	}
	var node types.Object
	diags := get(ctx, path.Root("node"), &node)
	if diags.HasError() || node.IsNull() || node.IsUnknown() {
		return client, diags
	}
	var model NodeModel
	diags.Append(node.As(ctx, &model, basetypes.ObjectAsOptions{})...)
	if diags.HasError() || model.Host.IsUnknown() || model.Port.IsUnknown() {
		return client, diags
	}
	port := uint16(0)
	if !model.Port.IsNull() {
		value := model.Port.ValueInt32()
		if value <= 0 || value > 65535 {
			diags.AddAttributeError(path.Root("node").AtName("port"), "Invalid port", fmt.Sprintf("Port %d is out of range.", value))
			return client, diags
		}
		port = uint16(value)
	}
	nodeClient, err := client.ForNode(model.Host.ValueString(), port)
	if err != nil {
		diags.AddError("Error connecting to node "+model.Name.ValueString(), fmt.Sprintf("%+v\n", err))
		return client, diags
	}
	return nodeClient, diags
}

// importNode reads the node part of an import ID: name@host[:port].
func importNode(ref string) (*NodeModel, error) {
	name, address, found := strings.Cut(ref, "@")
	if !found || name == "" || address == "" {
		return nil, fmt.Errorf("expected node@host[:port], got %q", ref)
	}
	model := &NodeModel{Name: types.StringValue(name), Host: types.StringValue(address), Port: types.Int32Null()}
	if host, port, hasPort := strings.Cut(address, ":"); hasPort {
		var value int32
		if _, err := fmt.Sscanf(port, "%d", &value); err != nil || value <= 0 || value > 65535 {
			return nil, fmt.Errorf("invalid port in %q", ref)
		}
		model.Host, model.Port = types.StringValue(host), types.Int32Value(value)
	}
	return model, nil
}

func nodeValue(ctx context.Context, model *NodeModel) (types.Object, diag.Diagnostics) {
	return types.ObjectValueFrom(ctx, nodeAttrTypes(), model)
}

// Converger describes how one kind of schema object is read, created and changed in place.
type Converger[T any] struct {
	// Kind and QualifiedName identify the object in error messages.
	Kind          string
	QualifiedName string
	Get           func(ctx context.Context) (*T, error)
	Create        func(ctx context.Context) error
	Reconcile     func(ctx context.Context, existing *T) error
}

// Converge creates the object, or changes the existing one in place. When adopt is false, an
// object that already exists is an error. It returns the object as the server reports it after.
func Converge[T any](ctx context.Context, adopt bool, converger Converger[T]) (*T, diag.Diagnostics) {
	var diags diag.Diagnostics

	existing, err := converger.Get(ctx)
	if err != nil {
		diags.AddError("Error reading "+converger.Kind, fmt.Sprintf("%+v\n", err))
		return nil, diags
	}
	switch {
	case existing == nil:
		err = converger.Create(ctx)
	case !adopt:
		diags.AddError(
			fmt.Sprintf("%s already exists", titleCase(converger.Kind)),
			fmt.Sprintf("The %s %s already exists. Import it, or set adopt_existing = true in the provider configuration.", converger.Kind, converger.QualifiedName),
		)
		return nil, diags
	default:
		err = converger.Reconcile(ctx, existing)
	}
	if err != nil {
		diags.AddError(fmt.Sprintf("Error applying %s %s", converger.Kind, converger.QualifiedName), err.Error())
		return nil, diags
	}

	object, err := converger.Get(ctx)
	if err != nil {
		diags.AddError("Error reading "+converger.Kind, fmt.Sprintf("%+v\n", err))
		return nil, diags
	}
	if object == nil {
		diags.AddError("Error reading "+converger.Kind, fmt.Sprintf("The %s %s was not found after it was applied.", converger.Kind, converger.QualifiedName))
		return nil, diags
	}
	return object, diags
}

// PlanObject reads the object and derives the state it would have if it were read now. exists is
// false when the server does not have it; converged is true when that state is the desired one.
func PlanObject[M any, T any](
	ctx context.Context, desired M,
	get func(context.Context) (*T, error),
	sync func(context.Context, *M, *T) diag.Diagnostics,
) (*M, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	object, err := get(ctx)
	if err != nil {
		diags.AddError("Error reading object", err.Error())
		return nil, false, diags
	}
	if object == nil {
		return nil, false, diags
	}
	candidate := desired
	diags.Append(sync(ctx, &candidate, object)...)
	return &candidate, reflect.DeepEqual(candidate, desired), diags
}

// ReadObject reads the object and derives the new state with sync. It returns a nil state when
// the server does not have the object.
func ReadObject[M any, T any](
	ctx context.Context, state M,
	get func(ctx context.Context) (*T, error),
	sync func(ctx context.Context, state *M, object *T) diag.Diagnostics,
) (*M, *T, diag.Diagnostics) {
	var diags diag.Diagnostics
	object, err := get(ctx)
	if err != nil {
		diags.AddError("Error reading object", fmt.Sprintf("%+v\n", err))
		return nil, nil, diags
	}
	if object == nil {
		return nil, nil, diags
	}
	diags.Append(sync(ctx, &state, object)...)
	return &state, object, diags
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
// all of them are equivalent, nothing will run, and the create statement keeps its value too.
func KeepEquivalentStrings(ctx context.Context, plan *tfsdk.Plan, stateCreateStatement types.String, attributes []EquivalentString) diag.Diagnostics {
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

	if unchanged {
		diags.Append(plan.SetAttribute(ctx, path.Root("create_statement"), stateCreateStatement)...)
	}

	return diags
}
