package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Model describes the provider data model.
type Model struct {
	Protocol              types.String `tfsdk:"protocol"`
	Host                  types.String `tfsdk:"host"`
	Port                  types.Int32  `tfsdk:"port"`
	AuthConfig            AuthConfig   `tfsdk:"auth_config"`
	TLSConfig             *TLSConfig   `tfsdk:"tls_config"`
	ReadAfterWriteTimeout types.Int64  `tfsdk:"read_after_write_timeout"`
	DialTimeout           types.Int64  `tfsdk:"dial_timeout"`
	QueryTimeout          types.Int64  `tfsdk:"query_timeout"`
	FanoutCluster         types.String `tfsdk:"fanout_cluster"`
	AdoptExisting         types.Bool   `tfsdk:"adopt_existing"`
	IgnoreColumnOrder     types.Bool   `tfsdk:"ignore_column_order"`
}

type AuthConfig struct {
	Strategy types.String `tfsdk:"strategy"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
}

type TLSConfig struct {
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	CACert             types.String `tfsdk:"ca_cert"`
	ServerName         types.String `tfsdk:"server_name"`
}
