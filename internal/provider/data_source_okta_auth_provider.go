package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

var (
	_ datasource.DataSource                     = &oktaAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigValidators = &oktaAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigure        = &oktaAuthProviderDataSource{}
)

// NewOktaAuthProviderDataSource returns a new
// firezone_okta_auth_provider data source instance, for use with
// FirezoneProvider.DataSources.
//
// Auth providers are configured in the Firezone dashboard, which owns
// the OAuth secrets involved, so there is no matching resource. This
// data source exists mainly to resolve a provider's ID for a
// firezone_policy condition on auth_provider_id.
func NewOktaAuthProviderDataSource() datasource.DataSource {
	return &oktaAuthProviderDataSource{}
}

type oktaAuthProviderDataSource struct {
	client *firezone.Client
}

// oktaAuthProviderDataSourceModel mirrors the
// firezone_okta_auth_provider data source schema.
type oktaAuthProviderDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Issuer     types.String `tfsdk:"issuer"`
	IsDisabled types.Bool   `tfsdk:"is_disabled"`
	IsDefault  types.Bool   `tfsdk:"is_default"`
	ClientID   types.String `tfsdk:"client_id"`
	OktaDomain types.String `tfsdk:"okta_domain"`
}

func (d *oktaAuthProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_okta_auth_provider"
}

func (d *oktaAuthProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Okta auth provider by id or name. Exactly one of the two " +
			"must be set. Read-only: auth providers are configured in the Firezone dashboard.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Auth provider ID. Set this or name, not both. This is the value a " +
					"firezone_policy condition on auth_provider_id takes.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Auth provider name, as shown in the dashboard's authentication " +
					"settings. Set this or id, not both. Fails if more than one provider shares " +
					"this name.",
			},
			"issuer": schema.StringAttribute{
				Computed:    true,
				Description: "The provider's issuer identifier.",
			},
			"is_disabled": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the provider is disabled. A disabled provider cannot be " +
					"used to sign in, but Policies may still reference it.",
			},
			"is_default": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether this provider is the account's default sign-in method.",
			},
			"client_id": schema.StringAttribute{
				Computed:    true,
				Description: "OAuth client ID. Not a secret; the client secret is never returned by the API.",
			},
			"okta_domain": schema.StringAttribute{
				Computed:    true,
				Description: "Okta domain this provider authenticates against.",
			},
		},
	}
}

func (d *oktaAuthProviderDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *oktaAuthProviderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *oktaAuthProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config oktaAuthProviderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var provider *firezone.OktaAuthProvider
	if id := config.ID.ValueString(); id != "" {
		found, err := d.client.OktaAuthProviders.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Okta auth provider", err.Error())
			return
		}
		provider = found
	} else {
		matches, err := findOktaAuthProvidersByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Okta auth provider", err.Error())
			return
		}

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Okta auth provider Not Found",
				fmt.Sprintf("No Okta auth provider found with name %q.", config.Name.ValueString()))
			return
		case 1:
			provider = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError("Ambiguous Okta auth provider Name",
				fmt.Sprintf("%d Okta auth providers are named %q: %s. Look the provider up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")))
			return
		}
	}

	config.ID = types.StringValue(provider.ID)
	config.Name = types.StringValue(provider.Name)
	config.Issuer = types.StringValue(provider.Issuer)
	config.IsDisabled = types.BoolValue(provider.IsDisabled)
	config.IsDefault = types.BoolValue(provider.IsDefault)
	config.ClientID = types.StringValue(provider.ClientID)
	config.OktaDomain = types.StringValue(provider.OktaDomain)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findOktaAuthProvidersByName asks the API for every Okta auth provider with this
// exact name. The name filter does the matching server-side, so this
// normally makes a single request - it loops pages only because provider
// names aren't unique and enough matches could span one.
func findOktaAuthProvidersByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.OktaAuthProvider, error) {
	var matches []firezone.OktaAuthProvider

	opts := &firezone.AuthProviderListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
	}
	for {
		page, err := client.OktaAuthProviders.List(ctx, opts)
		if err != nil {
			return nil, err
		}
		matches = append(matches, page.Data...)
		if page.Metadata.NextPage == "" {
			return matches, nil
		}
		opts.PageCursor = page.Metadata.NextPage
	}
}
