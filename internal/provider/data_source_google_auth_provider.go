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

	firezone "github.com/firezone/firezone-go"
)

var (
	_ datasource.DataSource                     = &googleAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigValidators = &googleAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigure        = &googleAuthProviderDataSource{}
)

// NewGoogleAuthProviderDataSource returns a new
// firezone_google_auth_provider data source instance, for use with
// FirezoneProvider.DataSources.
//
// Auth providers are configured in the Firezone dashboard, which owns
// the OAuth secrets involved, so there is no matching resource. This
// data source exists mainly to resolve a provider's ID for a
// firezone_policy condition on auth_provider_id.
func NewGoogleAuthProviderDataSource() datasource.DataSource {
	return &googleAuthProviderDataSource{}
}

type googleAuthProviderDataSource struct {
	client *firezone.Client
}

// googleAuthProviderDataSourceModel mirrors the
// firezone_google_auth_provider data source schema.
type googleAuthProviderDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Issuer     types.String `tfsdk:"issuer"`
	IsDisabled types.Bool   `tfsdk:"is_disabled"`
	IsDefault  types.Bool   `tfsdk:"is_default"`
}

func (d *googleAuthProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_google_auth_provider"
}

func (d *googleAuthProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Google auth provider by id or name. Exactly one of the two " +
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
		},
	}
}

func (d *googleAuthProviderDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *googleAuthProviderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *googleAuthProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config googleAuthProviderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var provider *firezone.GoogleAuthProvider
	if id := config.ID.ValueString(); id != "" {
		found, err := d.client.GoogleAuthProviders.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Google auth provider", err.Error())
			return
		}
		provider = found
	} else {
		matches, err := findGoogleAuthProvidersByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Google auth provider", err.Error())
			return
		}

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Google auth provider Not Found",
				fmt.Sprintf("No Google auth provider found with name %q.", config.Name.ValueString()))
			return
		case 1:
			provider = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError("Ambiguous Google auth provider Name",
				fmt.Sprintf("%d Google auth providers are named %q: %s. Look the provider up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")))
			return
		}
	}

	config.ID = types.StringValue(provider.ID)
	config.Name = types.StringValue(provider.Name)
	config.Issuer = types.StringValue(provider.Issuer)
	config.IsDisabled = types.BoolValue(provider.IsDisabled)
	config.IsDefault = types.BoolValue(provider.IsDefault)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findGoogleAuthProvidersByName asks the API for every Google auth provider with this
// exact name. The name filter does the matching server-side, so this
// normally makes a single request - it loops pages only because provider
// names aren't unique and enough matches could span one.
func findGoogleAuthProvidersByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.GoogleAuthProvider, error) {
	var matches []firezone.GoogleAuthProvider

	opts := &firezone.AuthProviderListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
	}
	for {
		page, err := client.GoogleAuthProviders.List(ctx, opts)
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
