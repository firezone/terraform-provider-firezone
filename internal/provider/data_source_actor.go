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
	_ datasource.DataSource                     = &actorDataSource{}
	_ datasource.DataSourceWithConfigValidators = &actorDataSource{}
	_ datasource.DataSourceWithConfigure        = &actorDataSource{}
)

// NewActorDataSource returns a new firezone_actor data source instance,
// for use with FirezoneProvider.DataSources.
func NewActorDataSource() datasource.DataSource {
	return &actorDataSource{}
}

type actorDataSource struct {
	client *firezone.Client
}

// actorDataSourceModel mirrors the firezone_actor data source schema -
// a read-only subset of actorResourceModel's fields.
type actorDataSourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Email               types.String `tfsdk:"email"`
	Type                types.String `tfsdk:"type"`
	AllowEmailOTPSignIn types.Bool   `tfsdk:"allow_email_otp_sign_in"`
	Enabled             types.Bool   `tfsdk:"enabled"`
}

func (d *actorDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_actor"
}

func (d *actorDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Actor by id or name. Exactly one of id or name must be set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Actor ID. Set this or name, not both.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Actor name. Set this or id, not both. Actor names aren't unique - " +
					"if more than one Actor shares this name, the lookup fails asking you to " +
					"set email (or switch to id).",
			},
			"email": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Email address. Always populated in the result. When looking up by " +
					"name, set this too to disambiguate if more than one Actor shares that name.",
			},
			"type": schema.StringAttribute{
				Computed:    true,
				Description: "One of account_user, account_admin_user, service_account, api_client.",
			},
			"allow_email_otp_sign_in": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether this Actor may sign in via a one-time passcode emailed to them.",
			},
			"enabled": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the Actor is enabled.",
			},
		},
	}
}

func (d *actorDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *actorDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *actorDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config actorDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var actor *firezone.Actor
	if id := config.ID.ValueString(); id != "" {
		a, err := d.client.Actors.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Actor", err.Error())
			return
		}
		actor = a
	} else {
		email := config.Email.ValueString()

		matches, err := findActorsByName(ctx, d.client, config.Name.ValueString(), email)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Actor", err.Error())
			return
		}

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError(
				"Actor Not Found",
				fmt.Sprintf("No Actor found with name %q%s.", config.Name.ValueString(), emailFilterSuffix(email)),
			)
			return
		case 1:
			actor = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = fmt.Sprintf("%s (email=%s)", m.ID, m.Email)
			}
			resp.Diagnostics.AddError(
				"Ambiguous Actor Name",
				fmt.Sprintf("%d Actors are named %q%s: %s. Set email to disambiguate, or look the Actor up by id instead.",
					len(matches), config.Name.ValueString(), emailFilterSuffix(email), strings.Join(ids, ", ")),
			)
			return
		}
	}

	config.ID = types.StringValue(actor.ID)
	config.Name = types.StringValue(actor.Name)
	config.Email = types.StringValue(actor.Email)
	config.Type = types.StringValue(string(actor.Type))
	config.AllowEmailOTPSignIn = types.BoolValue(actor.AllowEmailOTPSignIn)
	config.Enabled = types.BoolValue(!actor.IsDisabled())

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findActorsByName asks the API for every Actor with this exact name
// (and, if email is non-empty, this exact email too). The name/email
// filters do the exact matching server-side; this only loops pages in
// case more Actors match than fit on one page.
func findActorsByName(ctx context.Context, client *firezone.Client, name, email string) ([]firezone.Actor, error) {
	var matches []firezone.Actor

	opts := &firezone.ActorListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
		Email:       email,
	}
	for {
		page, err := client.Actors.List(ctx, opts)
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

func emailFilterSuffix(email string) string {
	if email == "" {
		return ""
	}
	return fmt.Sprintf(" with email = %q", email)
}
