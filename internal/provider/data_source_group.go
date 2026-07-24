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
	_ datasource.DataSource                     = &groupDataSource{}
	_ datasource.DataSourceWithConfigValidators = &groupDataSource{}
	_ datasource.DataSourceWithConfigure        = &groupDataSource{}
)

// NewGroupDataSource returns a new firezone_group data source instance,
// for use with FirezoneProvider.DataSources.
//
// This is the intended way to reference Groups synced from an identity
// provider - they're read-only via the API, so they can't be created
// as a firezone_group resource; look them up here instead.
func NewGroupDataSource() datasource.DataSource {
	return &groupDataSource{}
}

type groupDataSource struct {
	client *firezone.Client
}

// groupDataSourceModel mirrors the firezone_group data source schema.
// Unlike groupResourceModel, it carries DirectoryID - a Group synced
// from an identity provider can share its name with an unrelated
// unsynced Group, or with a Group synced from a different directory,
// and DirectoryID is how a lookup by name disambiguates between them.
type groupDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	DirectoryID types.String `tfsdk:"directory_id"`
}

func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Group by id or name, including Groups synced from an identity provider. Exactly one of id or name must be set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Group ID. Set this or name, not both.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Group name. Set this or id, not both. If more than one Group " +
					"shares this name, the lookup fails asking you to set directory_id (or " +
					"switch to id).",
			},
			"directory_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Directory this Group is synced from, or \"\" for a Group native " +
					"to Firezone (not synced). Only used to disambiguate a lookup by name: " +
					"if two Groups share a name (e.g. a native Group and one synced in from an " +
					"identity provider, or Groups of the same name synced from two different " +
					"directories), set this to the specific directory_id you want, or to \"\" " +
					"to select the native, unsynced Group. Leave unset to match regardless of " +
					"directory, which fails if the name is ambiguous. Always populated in the " +
					"result, regardless of how the Group was looked up.",
			},
		},
	}
}

func (d *groupDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *groupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config groupDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var group *firezone.Group
	if id := config.ID.ValueString(); id != "" {
		g, err := d.client.Groups.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Group", err.Error())
			return
		}
		group = g
	} else {
		var directoryFilter *string
		if !config.DirectoryID.IsNull() {
			v := config.DirectoryID.ValueString()
			directoryFilter = &v
		}

		matches, err := findGroupsByName(ctx, d.client, config.Name.ValueString(), directoryFilter)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Group", err.Error())
			return
		}

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError(
				"Group Not Found",
				fmt.Sprintf("No Group found with name %q%s.", config.Name.ValueString(), directoryFilterSuffix(directoryFilter)),
			)
			return
		case 1:
			group = &matches[0]
		default:
			resp.Diagnostics.AddError(
				"Ambiguous Group Name",
				fmt.Sprintf(
					"%d Groups are named %q%s: %s. Set directory_id to disambiguate "+
						"(\"\" selects the unsynced, native Group), or look the Group up by id instead.",
					len(matches), config.Name.ValueString(), directoryFilterSuffix(directoryFilter), describeGroupMatches(matches),
				),
			)
			return
		}
	}

	config.ID = types.StringValue(group.ID)
	config.Name = types.StringValue(group.Name)
	config.DirectoryID = types.StringValue(group.DirectoryID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findGroupsByName asks the API for every Group with this exact name
// (and, if directoryFilter is non-nil, this exact DirectoryID - ""
// matches unsynced Groups). The name/directory_id filters do the exact
// matching server-side; this only loops pages in case more Groups
// match than fit on one page, which is unusual but not impossible.
func findGroupsByName(ctx context.Context, client *firezone.Client, name string, directoryFilter *string) ([]firezone.Group, error) {
	var matches []firezone.Group

	opts := &firezone.GroupListOptions{
		ListOptions: firezone.ListOptions{Limit: 100},
		Name:        name,
		DirectoryID: directoryFilter,
	}
	for {
		page, err := client.Groups.List(ctx, opts)
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

func directoryFilterSuffix(directoryFilter *string) string {
	if directoryFilter == nil {
		return ""
	}
	if *directoryFilter == "" {
		return " with directory_id = \"\" (unsynced)"
	}
	return fmt.Sprintf(" with directory_id = %q", *directoryFilter)
}

func describeGroupMatches(matches []firezone.Group) string {
	descriptions := make([]string, len(matches))
	for i, g := range matches {
		directory := g.DirectoryID
		if directory == "" {
			directory = "unsynced"
		}
		descriptions[i] = fmt.Sprintf("%s (directory_id=%s)", g.ID, directory)
	}
	return strings.Join(descriptions, ", ")
}
