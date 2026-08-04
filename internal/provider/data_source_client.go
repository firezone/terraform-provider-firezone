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
					"more than one matches. Lookup by name paginates every Client in the " +
					"account client-side, since the API has no filter parameters on this " +
					"endpoint; prefer id for large accounts.",
			},
			"firezone_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "The device's stable Firezone ID. Set exactly one of id, name, or " +
					"firezone_id. Unlike name, this is unique per device and survives a " +
					"rename, which makes it the better key for a long-lived config. Carries " +
					"the same client-side pagination cost as name.",
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

	// Neither name nor firezone_id has a server-side filter, so both
	// scan the full Client list. Describing the search in one place
	// keeps the two error messages consistent.
	var (
		attribute string
		wanted    string
		match     func(firezone.ClientDevice) bool
	)
	if name := config.Name.ValueString(); name != "" {
		attribute, wanted = "name", name
		match = func(c firezone.ClientDevice) bool { return c.Name == name }
	} else {
		firezoneID := config.FirezoneID.ValueString()
		attribute, wanted = "firezone_id", firezoneID
		match = func(c firezone.ClientDevice) bool { return c.FirezoneID == firezoneID }
	}

	matches, err := findClientDevices(ctx, d.client, match)
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

// findClientDevices paginates the full Client list, collecting every
// device satisfying match. The clients endpoint has no filter
// parameters at all - not by name, actor, or firezone_id - so every
// non-id lookup is a full scan. Acceptable for a data source read once
// per plan; it is not a hot path.
func findClientDevices(ctx context.Context, client *firezone.Client, match func(firezone.ClientDevice) bool) ([]firezone.ClientDevice, error) {
	var matches []firezone.ClientDevice

	opts := &firezone.ListOptions{Limit: 100}
	for {
		page, err := client.ClientDevices.List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, device := range page.Data {
			if match(device) {
				matches = append(matches, device)
			}
		}
		if page.Metadata.NextPage == "" {
			return matches, nil
		}
		opts.PageCursor = page.Metadata.NextPage
	}
}
