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
	_ datasource.DataSource                     = &googleDirectoryDataSource{}
	_ datasource.DataSourceWithConfigValidators = &googleDirectoryDataSource{}
	_ datasource.DataSourceWithConfigure        = &googleDirectoryDataSource{}
)

// NewGoogleDirectoryDataSource returns a new firezone_google_directory
// data source instance, for use with FirezoneProvider.DataSources.
//
// Directories are read-only via the API - this is how to look up a
// directory's id, e.g. to pass as firezone_group's directory_id when
// disambiguating Groups that share a name across directories.
func NewGoogleDirectoryDataSource() datasource.DataSource {
	return &googleDirectoryDataSource{}
}

type googleDirectoryDataSource struct {
	client *firezone.Client
}

type googleDirectoryDataSourceModel struct {
	ID     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Domain types.String `tfsdk:"domain"`
}

func (d *googleDirectoryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_google_directory"
}

func (d *googleDirectoryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Google Workspace directory connection by id or name. Exactly one of id or name must be set.",
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
					"or id, not both. Fails if more than one directory shares this name.",
			},
			"domain": schema.StringAttribute{
				Computed:    true,
				Description: "Google Workspace domain.",
			},
		},
	}
}

func (d *googleDirectoryDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *googleDirectoryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *googleDirectoryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config googleDirectoryDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var dir *firezone.GoogleDirectory
	if id := config.ID.ValueString(); id != "" {
		d2, err := d.client.GoogleDirectories.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Google Directory", err.Error())
			return
		}
		dir = d2
	} else {
		matches, err := findGoogleDirectoriesByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Google Directory", err.Error())
			return
		}
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Google Directory Not Found", fmt.Sprintf("No Google Workspace directory found with name %q.", config.Name.ValueString()))
			return
		case 1:
			dir = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError(
				"Ambiguous Google Directory Name",
				fmt.Sprintf("%d Google Workspace directories are named %q: %s. Look the directory up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")),
			)
			return
		}
	}

	config.ID = types.StringValue(dir.ID)
	config.Name = types.StringValue(dir.Name)
	config.Domain = types.StringValue(dir.Domain)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findGoogleDirectoriesByName asks the API for every Google directory
// with this exact name. The name filter does the matching server-side,
// so this normally makes a single request - it loops pages only because
// directory names aren't unique and enough matches could span one.
func findGoogleDirectoriesByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.GoogleDirectory, error) {
	var matches []firezone.GoogleDirectory

	opts := &firezone.DirectoryListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
	}
	for {
		page, err := client.GoogleDirectories.List(ctx, opts)
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
