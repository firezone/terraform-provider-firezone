// Package provider implements the Firezone Terraform provider. It is
// intentionally unexported/unimportable - nothing outside main.go
// should depend on it.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-go"
)

// Ensure FirezoneProvider satisfies provider.Provider.
var _ provider.Provider = &FirezoneProvider{}

// FirezoneProvider is the Firezone Terraform provider.
type FirezoneProvider struct {
	// version is injected at build time (see main.go) and sent as part
	// of the API client's User-Agent header.
	version string
}

// firezoneProviderModel mirrors the provider "firezone" { ... } config
// block schema.
type firezoneProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Token    types.String `tfsdk:"token"`
}

// New returns a factory for the Firezone provider, for use with
// providerserver.Serve.
func New() func() provider.Provider {
	return func() provider.Provider {
		return &FirezoneProvider{}
	}
}

// Metadata implements provider.Provider.
func (p *FirezoneProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "firezone"
}

// Schema implements provider.Provider.
func (p *FirezoneProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages Firezone (https://www.firezone.dev) resources via its REST API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				Description: "The bare Firezone API host, e.g. https://api.firezone.dev. " +
					"Defaults to the FIREZONE_ENDPOINT environment variable.",
			},
			"token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Bearer token for an api_client actor. Defaults to the " +
					"FIREZONE_TOKEN environment variable.",
			},
		},
	}
}

// Configure implements provider.Provider. It builds the *firezone.Client
// shared by every resource and data source's Configure method.
func (p *FirezoneProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config firezoneProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := config.Endpoint.ValueString()
	if endpoint == "" {
		endpoint = os.Getenv("FIREZONE_ENDPOINT")
	}
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Missing Firezone API Endpoint",
			"Set the endpoint attribute or the FIREZONE_ENDPOINT environment variable.",
		)
	}

	token := config.Token.ValueString()
	if token == "" {
		token = os.Getenv("FIREZONE_TOKEN")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing Firezone API Token",
			"Set the token attribute or the FIREZONE_TOKEN environment variable.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	userAgent := "terraform-provider-firezone"
	if p.version != "" {
		userAgent += "/" + p.version
	}

	client, err := firezone.NewClient(endpoint, token, firezone.WithUserAgent(userAgent))
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Firezone API Client", err.Error())
		return
	}

	resp.ResourceData = client
	resp.DataSourceData = client
}

// Resources implements provider.Provider.
func (p *FirezoneProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewSiteResource,
		NewResourceResource,
		NewPolicyResource,
		NewGroupResource,
		NewGroupMembershipResource,
		NewActorResource,
		NewGatewayResource,
		NewPoolMemberResource,
	}
}

// DataSources implements provider.Provider.
func (p *FirezoneProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewSiteDataSource,
		NewGroupDataSource,
		NewActorDataSource,
		NewClientDataSource,
		NewEntraDirectoryDataSource,
		NewGoogleDirectoryDataSource,
		NewOktaDirectoryDataSource,
	}
}
