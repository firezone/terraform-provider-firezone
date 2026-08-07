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
	_ datasource.DataSource                     = &emailOTPAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigValidators = &emailOTPAuthProviderDataSource{}
	_ datasource.DataSourceWithConfigure        = &emailOTPAuthProviderDataSource{}
)

// NewEmailOTPAuthProviderDataSource returns a new
// firezone_email_otp_auth_provider data source instance, for use with
// FirezoneProvider.DataSources.
//
// Auth providers are configured in the Firezone dashboard, which owns
// the OAuth secrets involved, so there is no matching resource. This
// data source exists mainly to resolve a provider's ID for a
// firezone_policy condition on auth_provider_id.
func NewEmailOTPAuthProviderDataSource() datasource.DataSource {
	return &emailOTPAuthProviderDataSource{}
}

type emailOTPAuthProviderDataSource struct {
	client *firezone.Client
}

// emailOTPAuthProviderDataSourceModel mirrors the
// firezone_email_otp_auth_provider data source schema.
type emailOTPAuthProviderDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Issuer     types.String `tfsdk:"issuer"`
	IsDisabled types.Bool   `tfsdk:"is_disabled"`
}

func (d *emailOTPAuthProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_email_otp_auth_provider"
}

func (d *emailOTPAuthProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Email OTP auth provider by id or name. Exactly one of the two " +
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
		},
	}
}

func (d *emailOTPAuthProviderDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *emailOTPAuthProviderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *emailOTPAuthProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config emailOTPAuthProviderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var provider *firezone.EmailOTPAuthProvider
	if id := config.ID.ValueString(); id != "" {
		found, err := d.client.EmailOTPAuthProviders.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Email OTP auth provider", err.Error())
			return
		}
		provider = found
	} else {
		matches, err := findEmailOTPAuthProvidersByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Email OTP auth provider", err.Error())
			return
		}

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Email OTP auth provider Not Found",
				fmt.Sprintf("No Email OTP auth provider found with name %q.", config.Name.ValueString()))
			return
		case 1:
			provider = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError("Ambiguous Email OTP auth provider Name",
				fmt.Sprintf("%d Email OTP auth providers are named %q: %s. Look the provider up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")))
			return
		}
	}

	config.ID = types.StringValue(provider.ID)
	config.Name = types.StringValue(provider.Name)
	config.Issuer = types.StringValue(provider.Issuer)
	config.IsDisabled = types.BoolValue(provider.IsDisabled)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findEmailOTPAuthProvidersByName asks the API for every Email OTP auth provider with this
// exact name. The name filter does the matching server-side, so this
// normally makes a single request - it loops pages only because provider
// names aren't unique and enough matches could span one.
func findEmailOTPAuthProvidersByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.EmailOTPAuthProvider, error) {
	var matches []firezone.EmailOTPAuthProvider

	opts := &firezone.AuthProviderListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
	}
	for {
		page, err := client.EmailOTPAuthProviders.List(ctx, opts)
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
