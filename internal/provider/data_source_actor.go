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
		Description: "Looks up an existing Actor by id, name, or email. Set id, or set name and/or " +
			"email - id can't be combined with either.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Actor ID. Can't be combined with name or email.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Actor name. Can't be combined with id. Actor names aren't unique - " +
					"if more than one Actor shares this name, the lookup fails asking you to " +
					"set email (or switch to id).",
			},
			"email": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Email address. Can't be combined with id. Unique per account and " +
					"stable across renames, so it works as a lookup key on its own; combine it " +
					"with name to disambiguate Actors sharing a name. Always populated in the result.",
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

// ConfigValidators allows id, name, email, or name+email - but never id
// alongside either of the others. Deliberately not ExactlyOneOf(id, name,
// email): name+email is a supported combination, used to disambiguate
// Actors sharing a name.
func (d *actorDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.AtLeastOneOf(
			path.MatchRoot("id"), path.MatchRoot("name"), path.MatchRoot("email"),
		),
		datasourcevalidator.Conflicting(path.MatchRoot("id"), path.MatchRoot("name")),
		datasourcevalidator.Conflicting(path.MatchRoot("id"), path.MatchRoot("email")),
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

		name := config.Name.ValueString()

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError(
				"Actor Not Found",
				fmt.Sprintf("No Actor found with %s.", actorFilterDescription(name, email)),
			)
			return
		case 1:
			actor = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = fmt.Sprintf("%s (email=%s)", m.ID, m.Email)
			}
			// Email is unique per account, so this branch is only
			// really reachable for a name-only lookup - but the advice
			// still has to make sense if email was already supplied.
			advice := "Set email to disambiguate, or look the Actor up by id instead."
			if email != "" {
				advice = "Look the Actor up by id instead."
			}
			resp.Diagnostics.AddError(
				"Ambiguous Actor Lookup",
				fmt.Sprintf("%d Actors match %s: %s. %s",
					len(matches), actorFilterDescription(name, email), strings.Join(ids, ", "), advice),
			)
			return
		}
	}

	config.ID = types.StringValue(actor.ID)
	config.Name = types.StringValue(actor.Name)
	config.Email = types.StringValue(actor.Email)
	config.Type = types.StringValue(string(actor.Type))
	config.AllowEmailOTPSignIn = types.BoolValue(actor.AllowEmailOTPSignIn)
	config.Enabled = types.BoolValue(!actor.IsDisabled)

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

// actorFilterDescription renders the filters actually supplied, so the
// not-found and ambiguous messages describe the query the practitioner
// wrote rather than assuming name is always set.
func actorFilterDescription(name, email string) string {
	switch {
	case name != "" && email != "":
		return fmt.Sprintf("name %q and email %q", name, email)
	case email != "":
		return fmt.Sprintf("email %q", email)
	default:
		return fmt.Sprintf("name %q", name)
	}
}
