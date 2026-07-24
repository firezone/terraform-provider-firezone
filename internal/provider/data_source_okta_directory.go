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
	_ datasource.DataSource                     = &oktaDirectoryDataSource{}
	_ datasource.DataSourceWithConfigValidators = &oktaDirectoryDataSource{}
	_ datasource.DataSourceWithConfigure        = &oktaDirectoryDataSource{}
)

// NewOktaDirectoryDataSource returns a new firezone_okta_directory data
// source instance, for use with FirezoneProvider.DataSources.
//
// Directories are read-only via the API - this is how to look up a
// directory's id, e.g. to pass as firezone_group's directory_id when
// disambiguating Groups that share a name across directories.
func NewOktaDirectoryDataSource() datasource.DataSource {
	return &oktaDirectoryDataSource{}
}

type oktaDirectoryDataSource struct {
	client *firezone.Client
}

type oktaDirectoryDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	OktaDomain types.String `tfsdk:"okta_domain"`
}

func (d *oktaDirectoryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_okta_directory"
}

func (d *oktaDirectoryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Okta directory connection by id or name. Exactly one of id or name must be set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Directory ID. Set this or name, not both. Use this value as firezone_group's directory_id.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Directory name, as shown in the Firezone dashboard's identity provider settings. Set this " +
					"or id, not both. Lookup by name requires paginating every Okta directory in the account " +
					"client-side (the API has no ?name= filter). Fails if more than one directory shares this name.",
			},
			"okta_domain": schema.StringAttribute{
				Computed:    true,
				Description: "Okta domain.",
			},
		},
	}
}

func (d *oktaDirectoryDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *oktaDirectoryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *oktaDirectoryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config oktaDirectoryDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var dir *firezone.OktaDirectory
	if id := config.ID.ValueString(); id != "" {
		d2, err := d.client.OktaDirectories.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Okta Directory", err.Error())
			return
		}
		dir = d2
	} else {
		matches, err := findOktaDirectoriesByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Okta Directory", err.Error())
			return
		}
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Okta Directory Not Found", fmt.Sprintf("No Okta directory found with name %q.", config.Name.ValueString()))
			return
		case 1:
			dir = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError(
				"Ambiguous Okta Directory Name",
				fmt.Sprintf("%d Okta directories are named %q: %s. Look the directory up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")),
			)
			return
		}
	}

	config.ID = types.StringValue(dir.ID)
	config.Name = types.StringValue(dir.Name)
	config.OktaDomain = types.StringValue(dir.OktaDomain)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findOktaDirectoriesByName paginates every Okta directory in the
// account looking for every exact name match, since the API has no
// ?name= query filter.
func findOktaDirectoriesByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.OktaDirectory, error) {
	var matches []firezone.OktaDirectory

	opts := &firezone.ListOptions{Limit: 100}
	for {
		page, err := client.OktaDirectories.List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for i := range page.Data {
			if page.Data[i].Name == name {
				matches = append(matches, page.Data[i])
			}
		}
		if page.Metadata.NextPage == "" {
			return matches, nil
		}
		opts.PageCursor = page.Metadata.NextPage
	}
}
