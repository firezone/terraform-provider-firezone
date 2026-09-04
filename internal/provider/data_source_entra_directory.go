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
	_ datasource.DataSource                     = &entraDirectoryDataSource{}
	_ datasource.DataSourceWithConfigValidators = &entraDirectoryDataSource{}
	_ datasource.DataSourceWithConfigure        = &entraDirectoryDataSource{}
)

// NewEntraDirectoryDataSource returns a new firezone_entra_directory
// data source instance, for use with FirezoneProvider.DataSources.
//
// Directories are read-only via the API - this is how to look up a
// directory's id, e.g. to pass as firezone_group's directory_id when
// disambiguating Groups that share a name across directories.
func NewEntraDirectoryDataSource() datasource.DataSource {
	return &entraDirectoryDataSource{}
}

type entraDirectoryDataSource struct {
	client *firezone.Client
}

type entraDirectoryDataSourceModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	TenantID types.String `tfsdk:"tenant_id"`
}

func (d *entraDirectoryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entra_directory"
}

func (d *entraDirectoryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Microsoft Entra directory connection by id or name. Exactly one of id or name must be set.",
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
			"tenant_id": schema.StringAttribute{
				Computed:    true,
				Description: "Microsoft Entra tenant ID.",
			},
		},
	}
}

func (d *entraDirectoryDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *entraDirectoryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *entraDirectoryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config entraDirectoryDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var dir *firezone.EntraDirectory
	if id := config.ID.ValueString(); id != "" {
		d2, err := d.client.EntraDirectories.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Entra Directory", err.Error())
			return
		}
		dir = d2
	} else {
		matches, err := findEntraDirectoriesByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Entra Directory", err.Error())
			return
		}
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Entra Directory Not Found", fmt.Sprintf("No Entra directory found with name %q.", config.Name.ValueString()))
			return
		case 1:
			dir = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError(
				"Ambiguous Entra Directory Name",
				fmt.Sprintf("%d Entra directories are named %q: %s. Look the directory up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")),
			)
			return
		}
	}

	config.ID = types.StringValue(dir.ID)
	config.Name = types.StringValue(dir.Name)
	config.TenantID = types.StringValue(dir.TenantID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findEntraDirectoriesByName asks the API for every Entra directory
// with this exact name. The name filter does the matching server-side,
// so this normally makes a single request - it loops pages only because
// directory names aren't unique and enough matches could span one.
func findEntraDirectoriesByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.EntraDirectory, error) {
	var matches []firezone.EntraDirectory

	opts := &firezone.DirectoryListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
	}
	for {
		page, err := client.EntraDirectories.List(ctx, opts)
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
