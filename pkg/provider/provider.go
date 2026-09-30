package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	tfresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/project"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/database"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/dictionary"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/grantprivilege"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/grantrole"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/maskingpolicy"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/materializedview"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/namedcollection"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/role"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/rowpolicy"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/setting"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/settingsprofile"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/settingsprofileassociation"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/table"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/tablecontents"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/user"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/view"
)

const (
	protocolNative       = "native"
	protocolNativeSecure = "nativesecure"
	protocolHTTP         = "http"
	protocolHTTPS        = "https"

	authStrategyPassword  = "password"
	authStrategyBasicAuth = "basicauth"

	defaultQueryTimeout = 300 * time.Second
)

var (
	availableProtocols      = []string{protocolNative, protocolNativeSecure, protocolHTTP, protocolHTTPS}
	availableAuthStrategies = []string{authStrategyPassword, authStrategyBasicAuth}
)

// Ensure Provider satisfies various provider interfaces.
var _ provider.Provider = &Provider{}

// Provider defines the provider implementation.
type Provider struct{}

func (p *Provider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "clickhousedbops"
	resp.Version = project.Version()
}

func (p *Provider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"protocol": schema.StringAttribute{
				Required:    true,
				Description: fmt.Sprintf("The protocol to use to connect to clickhouse instance. Valid options are: %s", strings.Join(availableProtocols, ", ")),
				Validators: []validator.String{
					stringvalidator.OneOf(availableProtocols...),
				},
			},
			"host": schema.StringAttribute{
				Required:    true,
				Description: "The hostname to use to connect to the clickhouse instance",
			},
			"port": schema.Int32Attribute{
				Required:    true,
				Description: "The port to use to connect to the clickhouse instance",
			},
			"auth_config": schema.SingleNestedAttribute{
				Attributes: map[string]schema.Attribute{
					"strategy": schema.StringAttribute{
						Required:    true,
						Description: "The authentication method to use",
						Validators: []validator.String{
							stringvalidator.OneOf(availableAuthStrategies...),
						},
					},
					"username": schema.StringAttribute{
						Required:    true,
						Description: "The username to use to authenticate to ClickHouse",
						Validators: []validator.String{
							stringvalidator.LengthAtLeast(1),
						},
					},
					"password": schema.StringAttribute{
						Optional:    true,
						Description: "The password to use to authenticate to ClickHouse",
						Validators: []validator.String{
							stringvalidator.LengthAtLeast(1),
						},
					},
				},
				Required:    true,
				Description: "Authentication configuration",
			},
			"tls_config": schema.SingleNestedAttribute{
				Attributes: map[string]schema.Attribute{
					"insecure_skip_verify": schema.BoolAttribute{
						Optional:    true,
						Description: "Skip TLS cert verification when using the https protocol. This is insecure!",
					},
					"ca_cert": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "PEM-encoded CA certificate to use for TLS verification. When specified, only this CA will be trusted for server certificate validation.",
					},
					"server_name": schema.StringAttribute{
						Optional:    true,
						Description: "Hostname to use for TLS SNI and certificate validation, if different from `host`. Useful when connecting through a tunnel or port-forward that resolves `host` to a different address but the server certificate is still issued for the original hostname.",
					},
				},
				Optional:    true,
				Description: "TLS configuration options",
			},
			"read_after_write_timeout": schema.Int64Attribute{
				Optional:    true,
				Description: "Timeout in seconds for read-after-write verification of created resources. ClickHouse Cloud services with multiple replicas may need higher values due to replication lag. Defaults to 30.",
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
			"dial_timeout": schema.Int64Attribute{
				Optional:    true,
				Description: "Timeout in seconds for establishing connections to ClickHouse. Useful when the ClickHouse instance takes time to start up from an idle state.",
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
			"fanout_cluster": schema.StringAttribute{
				Optional:    true,
				Description: "Name of a cluster in system.clusters. When set, the table, view, materialized_view and dictionary resources connect to every node of this cluster and run their DDL on each node, without ON CLUSTER. The nodes are reached at the host_address that system.clusters reports, which the server resolved from host_name, so the caller does not need to resolve node names; set tls_config.server_name when certificates are verified. They use the protocol, authentication, TLS and timeouts of this provider. The port is the one from system.clusters for the native protocol and the configured port for every other protocol.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"adopt_existing": schema.BoolAttribute{
				Optional:    true,
				Description: "When true, creating a table, view, materialized view or dictionary that already exists adopts it: the existing object is changed in place to match the configuration. When false (the default), creating an object that already exists is an error.",
			},
			"ignore_column_order": schema.BoolAttribute{
				Optional:    true,
				Description: "When true, a table whose columns match the configuration in a different order is not altered. New columns are still added after the column that precedes them in the configuration.",
			},
			"query_timeout": schema.Int64Attribute{
				Optional:    true,
				Description: "Timeout in seconds for each query ran against ClickHouse. Defaults to 300.",
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
		},
	}
}

// buildTLSConfig builds a *tls.Config from the provider's tls_config block.
func buildTLSConfig(cfg *TLSConfig) (*tls.Config, error) {
	tlsConfig := &tls.Config{} //nolint:gosec

	if cfg == nil {
		return tlsConfig, nil
	}

	if !cfg.InsecureSkipVerify.IsNull() {
		tlsConfig.InsecureSkipVerify = cfg.InsecureSkipVerify.ValueBool()
	}

	if !cfg.CACert.IsNull() && cfg.CACert.ValueString() != "" {
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM([]byte(cfg.CACert.ValueString())) {
			return nil, errors.New("failed to parse ca_cert as PEM-encoded certificate")
		}
		tlsConfig.RootCAs = caCertPool
	}

	if !cfg.ServerName.IsNull() && cfg.ServerName.ValueString() != "" {
		tlsConfig.ServerName = cfg.ServerName.ValueString()
	}

	return tlsConfig, nil
}

func (p *Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data Model
	var err error

	if !req.Config.Raw.IsFullyKnown() {
		if req.ClientCapabilities.DeferralAllowed {
			resp.Deferred = &provider.Deferred{Reason: provider.DeferredReasonProviderConfigUnknown}
			return
		}

		// Terraform configures the provider again with known values before apply.
		var dbopsClient dbops.Client
		dbopsClient, err = dbops.NewClient(clickhouseclient.NewUnknownConfigClient())
		if err != nil {
			resp.Diagnostics.AddError("error initializing dbops client", fmt.Sprintf("%+v\n", err))
			return
		}

		resp.ResourceData = dbopsClient
		resp.DataSourceData = dbopsClient
		return
	}

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	var dialTimeout time.Duration
	if !data.DialTimeout.IsNull() {
		dialTimeout = time.Duration(data.DialTimeout.ValueInt64()) * time.Second
	}

	queryTimeout := defaultQueryTimeout
	if !data.QueryTimeout.IsNull() {
		queryTimeout = time.Duration(data.QueryTimeout.ValueInt64()) * time.Second
	}

	var port uint16
	if !data.Port.IsUnknown() {
		portVal := data.Port.ValueInt32()
		if portVal <= 0 || portVal > 65535 {
			resp.Diagnostics.AddError("invalid configuration", fmt.Sprintf("invalid port %s.", data.Port.String()))
			return
		}

		port = uint16(portVal)
	}

	// newClickhouseClient opens a connection to one host with the provider's protocol,
	// authentication, TLS and timeouts.
	var newClickhouseClient func(host string, port uint16) (clickhouseclient.ClickhouseClient, error)
	switch data.Protocol.ValueString() {
	case protocolNative:
		fallthrough
	case protocolNativeSecure:
		var auth *clickhouseclient.UserPasswordAuth
		switch data.AuthConfig.Strategy.ValueString() {
		case authStrategyPassword:
			auth = &clickhouseclient.UserPasswordAuth{
				Username: data.AuthConfig.Username.ValueString(),
			}

			if !data.AuthConfig.Password.IsNull() {
				auth.Password = data.AuthConfig.Password.ValueString()
			}

			valid, errorStrings := auth.ValidateConfig()
			if !valid {
				resp.Diagnostics.AddError("invalid configuration", fmt.Sprintf("invalid authentication strategy configuration. %s", strings.Join(errorStrings, ", ")))
			}
		default:
			resp.Diagnostics.AddError("invalid configuration", fmt.Sprintf("invalid authentication strategy %q. %s protocol only supports %q", data.AuthConfig.Strategy, protocolNative, authStrategyPassword))
			return
		}

		var nativeTLSConfig *tls.Config
		if data.Protocol.ValueString() == protocolNativeSecure {
			var tlsErr error
			nativeTLSConfig, tlsErr = buildTLSConfig(data.TLSConfig)
			if tlsErr != nil {
				resp.Diagnostics.AddError("invalid configuration", tlsErr.Error())
				return
			}
		}

		newClickhouseClient = func(host string, port uint16) (clickhouseclient.ClickhouseClient, error) {
			return clickhouseclient.NewNativeClient(clickhouseclient.NativeClientConfig{
				Host:             host,
				Port:             port,
				UserPasswordAuth: auth,
				TLSConfig:        nativeTLSConfig,
				DialTimeout:      dialTimeout,
				QueryTimeout:     queryTimeout,
			})
		}
	case protocolHTTP:
		fallthrough
	case protocolHTTPS:
		var auth *clickhouseclient.BasicAuth
		switch data.AuthConfig.Strategy.ValueString() {
		case authStrategyBasicAuth:
			auth = &clickhouseclient.BasicAuth{
				Username: data.AuthConfig.Username.ValueString(),
			}

			if !data.AuthConfig.Password.IsNull() {
				auth.Password = data.AuthConfig.Password.ValueString()
			}

			valid, errorStrings := auth.ValidateConfig()
			if !valid {
				resp.Diagnostics.AddError("invalid configuration", fmt.Sprintf("invalid authentication strategy configuration. %s", strings.Join(errorStrings, ", ")))
			}
		default:
			resp.Diagnostics.AddError("invalid configuration", fmt.Sprintf("invalid authentication strategy %q. %s protocol only supports %q", data.AuthConfig.Strategy, protocolHTTP, authStrategyBasicAuth))
			return
		}

		var tlsConfig *tls.Config
		protocol := "http"
		if data.Protocol.ValueString() == protocolHTTPS {
			protocol = "https"
			var tlsErr error
			tlsConfig, tlsErr = buildTLSConfig(data.TLSConfig)
			if tlsErr != nil {
				resp.Diagnostics.AddError("invalid configuration", tlsErr.Error())
				return
			}
		}

		newClickhouseClient = func(host string, port uint16) (clickhouseclient.ClickhouseClient, error) {
			return clickhouseclient.NewHTTPClient(clickhouseclient.HTTPClientConfig{
				Protocol:     protocol,
				Host:         host,
				Port:         port,
				BasicAuth:    auth,
				TLSConfig:    tlsConfig,
				DialTimeout:  dialTimeout,
				QueryTimeout: queryTimeout,
			})
		}
	}

	clickhouseClient, err := newClickhouseClient(data.Host.ValueString(), port)
	if err != nil {
		resp.Diagnostics.AddError("error initializing clickhouse client", fmt.Sprintf("%+v\n", err))
		return
	}

	dbopsOpts := []dbops.ClientOption{
		dbops.WithHost(data.Host.ValueString()),
		dbops.WithAdoptExisting(data.AdoptExisting.ValueBool()),
		dbops.WithIgnoreColumnOrder(data.IgnoreColumnOrder.ValueBool()),
	}
	if !data.ReadAfterWriteTimeout.IsNull() {
		dbopsOpts = append(dbopsOpts, dbops.WithReadAfterWriteTimeout(time.Duration(data.ReadAfterWriteTimeout.ValueInt64())*time.Second))
	}
	if !data.FanoutCluster.IsNull() {
		dbopsOpts = append(dbopsOpts, dbops.WithFanout(data.FanoutCluster.ValueString(), func(address string, clusterPort uint16) (clickhouseclient.ClickhouseClient, error) {
			// system.clusters reports the native protocol port of each node. Every other
			// protocol uses the port of the provider configuration.
			if data.Protocol.ValueString() == protocolNative {
				return newClickhouseClient(address, clusterPort)
			}
			return newClickhouseClient(address, port)
		}))
	}

	dbopsClient, err := dbops.NewClient(clickhouseClient, dbopsOpts...)
	if err != nil {
		resp.Diagnostics.AddError("error initializing dbops client", fmt.Sprintf("%+v\n", err))
		return
	}

	resp.ResourceData = dbopsClient
	resp.DataSourceData = dbopsClient
}

func (p *Provider) Resources(ctx context.Context) []func() tfresource.Resource {
	return []func() tfresource.Resource{
		database.NewResource,
		dictionary.NewResource,
		table.NewResource,
		tablecontents.NewResource,
		view.NewResource,
		materializedview.NewResource,
		role.NewResource,
		user.NewResource,
		grantrole.NewResource,
		grantprivilege.NewResource,
		maskingpolicy.NewResource,
		settingsprofile.NewResource,
		setting.NewResource,
		settingsprofileassociation.NewResource,
		rowpolicy.NewResource,
		namedcollection.NewResource,
	}
}

func (p *Provider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func New() func() provider.Provider {
	return func() provider.Provider {
		return &Provider{}
	}
}
