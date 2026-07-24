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
	_ datasource.DataSource                     = &siteDataSource{}
	_ datasource.DataSourceWithConfigValidators = &siteDataSource{}
	_ datasource.DataSourceWithConfigure        = &siteDataSource{}
)

// NewSiteDataSource returns a new firezone_site data source instance,
// for use with FirezoneProvider.DataSources.
func NewSiteDataSource() datasource.DataSource {
	return &siteDataSource{}
}

type siteDataSource struct {
	client *firezone.Client
}

func (d *siteDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_site"
}

func (d *siteDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an existing Site by id or name. Exactly one of the two must be set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Site ID. Set this or name, not both.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Site name. Set this or id, not both. If more than one Site " +
					"shares this name, the lookup fails - use id instead.",
			},
		},
	}
}

func (d *siteDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *siteDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *siteDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config siteResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var site *firezone.Site
	if id := config.ID.ValueString(); id != "" {
		s, err := d.client.Sites.Get(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Site", err.Error())
			return
		}
		site = s
	} else {
		matches, err := findSitesByName(ctx, d.client, config.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading Site", err.Error())
			return
		}

		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Site Not Found", fmt.Sprintf("No Site found with name %q.", config.Name.ValueString()))
			return
		case 1:
			site = &matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			resp.Diagnostics.AddError(
				"Ambiguous Site Name",
				fmt.Sprintf("%d Sites are named %q: %s. Look the Site up by id instead.",
					len(matches), config.Name.ValueString(), strings.Join(ids, ", ")),
			)
			return
		}
	}

	config.ID = types.StringValue(site.ID)
	config.Name = types.StringValue(site.Name)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// findSitesByName asks the API for every Site with this exact name -
// the name filter does the exact matching server-side, so this only
// loops pages in case more Sites match than fit on one page.
func findSitesByName(ctx context.Context, client *firezone.Client, name string) ([]firezone.Site, error) {
	var matches []firezone.Site

	opts := &firezone.SiteListOptions{ListOptions: firezone.ListOptions{Limit: 100}, Name: name}
	for {
		page, err := client.Sites.List(ctx, opts)
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
