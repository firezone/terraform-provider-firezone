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
	_ datasource.DataSource                     = &entraAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigValidators = &entraAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigure        = &entraAuthProviderDataSource{}
)

// NewEntraAuthProviderDataSource returns a new
// firezone_entra_auth_provider data source instance, for use with
// FirezoneProvider.DataSources.
//
// Auth providers are configured in the Firezone dashboard, which owns
// the OAuth secrets involved, so there is no matching resource. This
// data source exists mainly to resolve a provider's ID for a
// firezone_policy condition on auth_provider_id.
func NewEntraAuthProviderDataSource() datasource.DataSource {
	return &entraAuthProviderDataSource{}
}

type entraAuthProviderDataSource struct {
	client *firezone.Client
}

// entraAuthProviderDataSourceModel mirrors the
// firezone_entra_auth_provider data source schema.
type entraAuthProviderDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Issuer     types.String `tfsdk:"issuer"`
	IsDisabled types.Bool   `tfsdk:"is_disabled"`
	IsDefault  types.Bool   `tfsdk:"is_default"`
	EmailClaim types.String `tfsdk:"email_claim"`
}

func (d *entraAuthProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entra_auth_provider"
}

func (d *entraAuthProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Entra auth provider by id or name. Exactly one of the two " +
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
			"email_claim": schema.StringAttribute{
				Computed:    true,
				Description: "Which token claim the provider's email address is read from.",
			},
		},
	}
}

func (d *entraAuthProviderDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *entraAuthProviderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *entraAuthProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config entraAuthProviderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var provider *firezone.EntraAuthProvider
	if id := config.ID.ValueString(); id != "" {
		found, err := d.client.EntraAuthProviders.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Entra auth provider", err.Error())
			return
		}
		provider = found
	} else {
		matches, err := findEntraAuthProvidersByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Entra auth provider", err.Error())
			return
		}

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Entra auth provider Not Found",
				fmt.Sprintf("No Entra auth provider found with name %q.", config.Name.ValueString()))
			return
		case 1:
			provider = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError("Ambiguous Entra auth provider Name",
				fmt.Sprintf("%d Entra auth providers are named %q: %s. Look the provider up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")))
			return
		}
	}

	config.ID = types.StringValue(provider.ID)
	config.Name = types.StringValue(provider.Name)
	config.Issuer = types.StringValue(provider.Issuer)
	config.IsDisabled = types.BoolValue(provider.IsDisabled)
	config.IsDefault = types.BoolValue(provider.IsDefault)
	config.EmailClaim = types.StringValue(provider.EmailClaim)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findEntraAuthProvidersByName asks the API for every Entra auth provider with this
// exact name. The name filter does the matching server-side, so this
// normally makes a single request - it loops pages only because provider
// names aren't unique and enough matches could span one.
func findEntraAuthProvidersByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.EntraAuthProvider, error) {
	var matches []firezone.EntraAuthProvider

	opts := &firezone.AuthProviderListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
	}
	for {
		page, err := client.EntraAuthProviders.List(ctx, opts)
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
