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
	_ datasource.DataSource                     = &clientDataSource{}
	_ datasource.DataSourceWithConfigValidators = &clientDataSource{}
	_ datasource.DataSourceWithConfigure        = &clientDataSource{}
)

// NewClientDataSource returns a new firezone_client data source
// instance, for use with FirezoneProvider.DataSources.
//
// There is no corresponding firezone_client resource: a device enrolls
// itself when it first connects, so Terraform can reference a Client but
// never create one. This data source exists mainly so
// firezone_pool_member can name Clients without hardcoding UUIDs.
func NewClientDataSource() datasource.DataSource {
	return &clientDataSource{}
}

type clientDataSource struct {
	client *firezone.Client
}

// clientDataSourceModel mirrors the firezone_client data source schema.
type clientDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	FirezoneID types.String `tfsdk:"firezone_id"`
	ActorID    types.String `tfsdk:"actor_id"`
	IPv4       types.String `tfsdk:"ipv4"`
	IPv6       types.String `tfsdk:"ipv6"`
	Online     types.Bool   `tfsdk:"online"`
	Verified   types.Bool   `tfsdk:"verified"`
}

func (d *clientDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_client"
}

func (d *clientDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an enrolled Client device by id, name, or firezone_id. Exactly " +
			"one of the three must be set. Clients enroll themselves on first connect, so " +
			"there is no firezone_client resource - this is lookup only.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Client ID. Set exactly one of id, name, or firezone_id.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Client name, as shown in the dashboard. Set exactly one of id, " +
					"name, or firezone_id. Client names are not unique - the lookup fails if " +
					"more than one matches.",
			},
			"firezone_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "The device's stable Firezone ID. Set exactly one of id, name, or " +
					"firezone_id. Unlike name, this survives a rename, which makes it the " +
					"better key for a long-lived config.",
			},
			"actor_id": schema.StringAttribute{
				Computed:    true,
				Description: "ID of the Actor this Client belongs to.",
			},
			"ipv4": schema.StringAttribute{
				Computed:    true,
				Description: "The Client's Firezone IPv4 address.",
			},
			"ipv6": schema.StringAttribute{
				Computed:    true,
				Description: "The Client's Firezone IPv6 address.",
			},
			"online": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the Client is currently connected. This is live state - " +
					"expect it to differ between plan and apply, and don't build " +
					"configuration decisions on it.",
			},
			"verified": schema.BoolAttribute{
				Computed: true,
				Description: "Whether an admin has verified this device. Policies can require " +
					"it via the client_verified condition property.",
			},
		},
	}
}

func (d *clientDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("name"),
			path.MatchRoot("firezone_id"),
		),
	}
}

func (d *clientDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *clientDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config clientDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	device, diags := d.lookup(ctx, config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.ID = types.StringValue(device.ID)
	config.Name = types.StringValue(device.Name)
	config.FirezoneID = types.StringValue(device.FirezoneID)
	config.ActorID = types.StringValue(device.ActorID)
	config.IPv4 = types.StringValue(device.IPv4)
	config.IPv6 = types.StringValue(device.IPv6)
	config.Online = types.BoolValue(device.Online)
	config.Verified = types.BoolValue(device.VerifiedAt != nil)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func (d *clientDataSource) lookup(ctx context.Context, config clientDataSourceModel) (*firezone.ClientDevice, fwDiagnostics) {
	var diags fwDiagnostics

	if id := config.ID.ValueString(); id != "" {
		device, err := d.client.ClientDevices.Get(ctx, id)
		if err != nil {
			diags.AddError("Error Reading Client", err.Error())
			return nil, diags
		}
		return device, diags
	}

	// Both lookups filter server-side. Neither key is unique, so the
	// result still needs the 0/1/many handling below - the filter only
	// removes the client-side scan, not the ambiguity.
	var (
		attribute string
		wanted    string
		opts      firezone.ClientListOptions
	)
	if name := config.Name.ValueString(); name != "" {
		attribute, wanted = "name", name
		opts.Name = name
	} else {
		firezoneID := config.FirezoneID.ValueString()
		attribute, wanted = "firezone_id", firezoneID
		opts.FirezoneID = firezoneID
	}

	matches, err := findClientDevices(ctx, d.client, opts)
	if err != nil {
		diags.AddError("Error Reading Client", err.Error())
		return nil, diags
	}

	switch len(matches) {
	case 0:
		diags.AddError("Client Not Found",
			fmt.Sprintf("No Client found with %s %q.", attribute, wanted))
		return nil, diags
	case 1:
		return &matches[0], diags
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		diags.AddError("Ambiguous Client",
			fmt.Sprintf("%d Clients have %s %q: %s. Look the Client up by id instead.",
				len(matches), attribute, wanted, strings.Join(ids, ", ")))
		return nil, diags
	}
}

// findClientDevices returns every Client matching opts' filters. The API
// does the matching, so this normally makes a single request - it still
// loops pages only because neither name nor firezone_id is unique, and
// enough matches could in principle span a page.
func findClientDevices(ctx context.Context, client *firezone.Client, opts firezone.ClientListOptions) ([]firezone.ClientDevice, error) {
	var matches []firezone.ClientDevice

	opts.Limit = 100
	for {
		page, err := client.ClientDevices.List(ctx, &opts)
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
