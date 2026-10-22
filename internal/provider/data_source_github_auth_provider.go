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
	_ datasource.DataSource                     = &gitHubAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigValidators = &gitHubAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigure        = &gitHubAuthProviderDataSource{}
)

// NewGitHubAuthProviderDataSource returns a new
// firezone_github_auth_provider data source instance, for use with
// FirezoneProvider.DataSources.
//
// Auth providers are configured in the Firezone dashboard, which owns
// the OAuth secrets involved, so there is no matching resource. This
// data source exists mainly to resolve a provider's ID for a
// firezone_policy condition on auth_provider_id.
func NewGitHubAuthProviderDataSource() datasource.DataSource {
	return &gitHubAuthProviderDataSource{}
}

type gitHubAuthProviderDataSource struct {
	client *firezone.Client
}

// gitHubAuthProviderDataSourceModel mirrors the
// firezone_github_auth_provider data source schema.
type gitHubAuthProviderDataSourceModel struct {
	ID                      types.String `tfsdk:"id"`
	Name                    types.String `tfsdk:"name"`
	Issuer                  types.String `tfsdk:"issuer"`
	IsDisabled              types.Bool   `tfsdk:"is_disabled"`
	IsDefault               types.Bool   `tfsdk:"is_default"`
	EmailVerificationMethod types.String `tfsdk:"email_verification_method"`
}

func (d *gitHubAuthProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_github_auth_provider"
}

func (d *gitHubAuthProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing GitHub auth provider by id or name. Exactly one of the two " +
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
			"email_verification_method": schema.StringAttribute{
				Computed: true,
				Description: "How a GitHub identity is first linked to an existing user by email: " +
					"proof emails a one-time code first, none links on a GitHub-verified email.",
			},
		},
	}
}

func (d *gitHubAuthProviderDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *gitHubAuthProviderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *gitHubAuthProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config gitHubAuthProviderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var provider *firezone.GitHubAuthProvider
	if id := config.ID.ValueString(); id != "" {
		found, err := d.client.GitHubAuthProviders.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading GitHub auth provider", err.Error())
			return
		}
		provider = found
	} else {
		matches, err := findGitHubAuthProvidersByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading GitHub auth provider", err.Error())
			return
		}

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("GitHub auth provider Not Found",
				fmt.Sprintf("No GitHub auth provider found with name %q.", config.Name.ValueString()))
			return
		case 1:
			provider = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError("Ambiguous GitHub auth provider Name",
				fmt.Sprintf("%d GitHub auth providers are named %q: %s. Look the provider up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")))
			return
		}
	}

	config.ID = types.StringValue(provider.ID)
	config.Name = types.StringValue(provider.Name)
	config.Issuer = types.StringValue(provider.Issuer)
	config.IsDisabled = types.BoolValue(provider.IsDisabled)
	config.IsDefault = types.BoolValue(provider.IsDefault)
	config.EmailVerificationMethod = types.StringValue(provider.EmailVerificationMethod)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findGitHubAuthProvidersByName asks the API for every GitHub auth provider with this
// exact name. The name filter does the matching server-side, so this
// normally makes a single request - it loops pages only because provider
// names aren't unique and enough matches could span one.
func findGitHubAuthProvidersByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.GitHubAuthProvider, error) {
	var matches []firezone.GitHubAuthProvider

	opts := &firezone.AuthProviderListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
	}
	for {
		page, err := client.GitHubAuthProviders.List(ctx, opts)
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
