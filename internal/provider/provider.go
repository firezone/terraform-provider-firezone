// Package provider implements the Firezone Terraform provider. It is
// intentionally unexported/unimportable - nothing outside main.go
// should depend on it.
package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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
	Endpoint     types.String `tfsdk:"endpoint"`
	Token        types.String `tfsdk:"token"`
	MaxRetries   types.Int64  `tfsdk:"max_retries"`
	RetryMaxWait types.Int64  `tfsdk:"retry_max_wait_seconds"`
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
			"max_retries": schema.Int64Attribute{
				Optional: true,
				Description: "How many times to retry a request rate limited with HTTP 429. " +
					"Waits honor the Retry-After header and add jitter. Defaults to the " +
					"FIREZONE_MAX_RETRIES environment variable, then to the API client's " +
					"own default. The API rate limits per account - roughly 20 requests of " +
					"burst refilling at one per second - so a large apply or destroy at " +
					"Terraform's default parallelism of 10 will be throttled; raise this, " +
					"or lower parallelism with -parallelism=N, if operations still fail " +
					"with 429.",
				Validators: []validator.Int64{
					int64validator.AtLeast(0),
				},
			},
			"retry_max_wait_seconds": schema.Int64Attribute{
				Optional: true,
				Description: "Caps how long any single rate-limit retry waits, in seconds. " +
					"Waits escalate exponentially up to this cap and never drop below the " +
					"Retry-After header. Defaults to the FIREZONE_RETRY_MAX_WAIT_SECONDS " +
					"environment variable, then to the API client's own default of 30. " +
					"Raising this buys more total patience than raising max_retries does, " +
					"since a bigger cap lengthens every later attempt.",
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
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

	opts := []firezone.Option{firezone.WithUserAgent(userAgent)}

	maxRetries, ok := resolveMaxRetries(config.MaxRetries, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if ok {
		opts = append(opts, firezone.WithRetry(maxRetries > 0, maxRetries))
	}

	retryMaxWait, ok := resolveRetryMaxWait(config.RetryMaxWait, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if ok {
		opts = append(opts, firezone.WithRetryMaxWait(retryMaxWait))
	}

	client, err := firezone.NewClient(endpoint, token, opts...)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Firezone API Client", err.Error())
		return
	}

	resp.ResourceData = client
	resp.DataSourceData = client
}

// resolveMaxRetries reads the retry budget from config, falling back to
// FIREZONE_MAX_RETRIES. ok is false when neither is set, leaving the API
// client on its own default rather than pinning it here - so the default
// lives in exactly one place.
func resolveMaxRetries(configured types.Int64, diags *diag.Diagnostics) (int, bool) {
	if !configured.IsNull() && !configured.IsUnknown() {
		return int(configured.ValueInt64()), true
	}

	raw := os.Getenv("FIREZONE_MAX_RETRIES")
	if raw == "" {
		return 0, false
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		diags.AddAttributeError(
			path.Root("max_retries"),
			"Invalid FIREZONE_MAX_RETRIES",
			fmt.Sprintf("Expected a non-negative integer, got: %q", raw),
		)
		return 0, false
	}
	return parsed, true
}

// resolveRetryMaxWait reads the per-retry wait cap from config, falling
// back to FIREZONE_RETRY_MAX_WAIT_SECONDS. As with resolveMaxRetries,
// ok is false when neither is set so the API client keeps its own
// default.
func resolveRetryMaxWait(configured types.Int64, diags *diag.Diagnostics) (time.Duration, bool) {
	if !configured.IsNull() && !configured.IsUnknown() {
		return time.Duration(configured.ValueInt64()) * time.Second, true
	}

	raw := os.Getenv("FIREZONE_RETRY_MAX_WAIT_SECONDS")
	if raw == "" {
		return 0, false
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 {
		diags.AddAttributeError(
			path.Root("retry_max_wait_seconds"),
			"Invalid FIREZONE_RETRY_MAX_WAIT_SECONDS",
			fmt.Sprintf("Expected a positive integer number of seconds, got: %q", raw),
		)
		return 0, false
	}
	return time.Duration(parsed) * time.Second, true
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
		NewEmailOTPAuthProviderDataSource,
		NewOIDCAuthProviderDataSource,
		NewGoogleAuthProviderDataSource,
		NewEntraAuthProviderDataSource,
		NewOktaAuthProviderDataSource,
		NewEntraDirectoryDataSource,
		NewGoogleDirectoryDataSource,
		NewOktaDirectoryDataSource,
	}
}
