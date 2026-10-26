package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

var (
	_ datasource.DataSource                   = &clientsDataSource{}
	_ datasource.DataSourceWithValidateConfig = &clientsDataSource{}
	_ datasource.DataSourceWithConfigure      = &clientsDataSource{}
)

// NewClientsDataSource returns a new firezone_clients data source
// instance, for use with FirezoneProvider.DataSources.
//
// Where firezone_client resolves exactly one device, this one returns
// every Client matching a set of filters - all of one Actor's devices,
// say, to feed firezone_pool_member with for_each.
func NewClientsDataSource() datasource.DataSource {
	return &clientsDataSource{}
}

type clientsDataSource struct {
	client *firezone.Client
}

// clientsDataSourceModel mirrors the firezone_clients data source schema.
type clientsDataSourceModel struct {
	ActorID        types.String        `tfsdk:"actor_id"`
	Name           types.String        `tfsdk:"name"`
	FirezoneID     types.String        `tfsdk:"firezone_id"`
	Online         types.Bool          `tfsdk:"online"`
	LastSeenBefore types.String        `tfsdk:"last_seen_before"`
	LastSeenAfter  types.String        `tfsdk:"last_seen_after"`
	ClientType     types.String        `tfsdk:"client_type"`
	Clients        []clientsEntryModel `tfsdk:"clients"`
}

// clientsEntryModel is one element of firezone_clients' clients list.
type clientsEntryModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	FirezoneID types.String `tfsdk:"firezone_id"`
	ActorID    types.String `tfsdk:"actor_id"`
	IPv4       types.String `tfsdk:"ipv4"`
	IPv6       types.String `tfsdk:"ipv6"`
	Online     types.Bool   `tfsdk:"online"`
	Verified   types.Bool   `tfsdk:"verified"`
	LastSeenAt types.String `tfsdk:"last_seen_at"`
}

func (d *clientsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_clients"
}

func (d *clientsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists enrolled Client devices, optionally filtered. Every filter is applied " +
			"by the API, and setting several returns only the Clients that match all of them. " +
			"Set no filters to list every Client in the account.",
		Attributes: map[string]schema.Attribute{
			"actor_id": schema.StringAttribute{
				Optional:    true,
				Description: "Only return Clients owned by this Actor.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Description: "Only return Clients with this exact name.",
			},
			"firezone_id": schema.StringAttribute{
				Optional:    true,
				Description: "Only return Clients with this Firezone ID.",
			},
			"online": schema.BoolAttribute{
				Optional: true,
				Description: "Set true to return only connected Clients, or false to return only " +
					"disconnected ones. Leave unset to ignore presence. This is live state, so " +
					"the result can differ between plan and apply.",
			},
			"last_seen_before": schema.StringAttribute{
				Optional: true,
				Description: "RFC 3339 timestamp, such as \"2026-01-01T00:00:00Z\". Only return " +
					"Clients whose latest connection began before this time. A Client that never " +
					"connected uses its creation time. Combine with online = false to leave out " +
					"Clients that are still connected on a long-running session.",
			},
			"last_seen_after": schema.StringAttribute{
				Optional: true,
				Description: "RFC 3339 timestamp. Only return Clients whose latest connection " +
					"began at or after this time. A Client that never connected uses its " +
					"creation time.",
			},
			"client_type": schema.StringAttribute{
				Optional: true,
				Description: "One of headless, gui, apple, android. Only return Clients whose " +
					"latest session came from this kind of Client application. A Client with " +
					"no recorded session matches no type.",
				Validators: []validator.String{
					stringvalidator.OneOf(
						string(firezone.ClientTypeHeadless),
						string(firezone.ClientTypeGUI),
						string(firezone.ClientTypeApple),
						string(firezone.ClientTypeAndroid),
					),
				},
			},
			"clients": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The matching Clients.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "Client ID.",
						},
						"name": schema.StringAttribute{
							Computed:    true,
							Description: "Client name, as shown in the dashboard.",
						},
						"firezone_id": schema.StringAttribute{
							Computed:    true,
							Description: "The device's stable Firezone ID.",
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
							Computed:    true,
							Description: "Whether the Client is currently connected.",
						},
						"verified": schema.BoolAttribute{
							Computed:    true,
							Description: "Whether an admin has verified this device.",
						},
						"last_seen_at": schema.StringAttribute{
							Computed: true,
							Description: "RFC 3339 time the Client last connected, or null if it " +
								"has never connected.",
						},
					},
				},
			},
		},
	}
}

// ValidateConfig checks the two timestamp filters parse, so a typo fails
// the plan with the attribute named rather than as a 422 at apply.
func (d *clientsDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var config clientsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	parseTimestampFilter(config.LastSeenBefore, path.Root("last_seen_before"), &resp.Diagnostics)
	parseTimestampFilter(config.LastSeenAfter, path.Root("last_seen_after"), &resp.Diagnostics)
}

func (d *clientsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *clientsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config clientsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := firezone.ClientListOptions{
		ActorID:        config.ActorID.ValueString(),
		Name:           config.Name.ValueString(),
		FirezoneID:     config.FirezoneID.ValueString(),
		ClientType:     firezone.ClientType(config.ClientType.ValueString()),
		LastSeenBefore: parseTimestampFilter(config.LastSeenBefore, path.Root("last_seen_before"), &resp.Diagnostics),
		LastSeenAfter:  parseTimestampFilter(config.LastSeenAfter, path.Root("last_seen_after"), &resp.Diagnostics),
	}
	if !config.Online.IsNull() {
		online := config.Online.ValueBool()
		opts.Online = &online
	}
	if resp.Diagnostics.HasError() {
		return
	}

	devices, err := findClientDevices(ctx, d.client, opts)
	if err != nil {
		resp.Diagnostics.AddError("Error Listing Clients", err.Error())
		return
	}

	config.Clients = make([]clientsEntryModel, len(devices))
	for i, device := range devices {
		lastSeenAt := types.StringNull()
		if device.LastSeenAt != nil {
			lastSeenAt = types.StringValue(device.LastSeenAt.Format(time.RFC3339))
		}
		config.Clients[i] = clientsEntryModel{
			ID:         types.StringValue(device.ID),
			Name:       types.StringValue(device.Name),
			FirezoneID: types.StringValue(device.FirezoneID),
			ActorID:    types.StringValue(device.ActorID),
			IPv4:       types.StringValue(device.IPv4),
			IPv6:       types.StringValue(device.IPv6),
			Online:     types.BoolValue(device.Online),
			Verified:   types.BoolValue(device.VerifiedAt != nil),
			LastSeenAt: lastSeenAt,
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// parseTimestampFilter parses an optional RFC 3339 filter attribute. It
// returns nil for a null or unknown value, and for an invalid one, after
// adding an error that names the attribute.
func parseTimestampFilter(value types.String, attr path.Path, diags *fwDiagnostics) *time.Time {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	t, err := time.Parse(time.RFC3339, value.ValueString())
	if err != nil {
		diags.AddAttributeError(attr, "Invalid Timestamp",
			fmt.Sprintf("%q is not an RFC 3339 timestamp, such as \"2026-01-01T00:00:00Z\".", value.ValueString()))
		return nil
	}
	return &t
}
