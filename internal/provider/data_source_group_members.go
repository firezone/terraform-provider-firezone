package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-sdk-go"
)

var (
	_ datasource.DataSource              = &groupMembersDataSource{}
	_ datasource.DataSourceWithConfigure = &groupMembersDataSource{}
)

// NewGroupMembersDataSource returns a new firezone_group_members data
// source instance, for use with FirezoneProvider.DataSources.
//
// It reads every member of a Group, including Groups synced from an
// identity provider and the managed Everyone Group, whose members
// firezone_group_membership cannot manage.
func NewGroupMembersDataSource() datasource.DataSource {
	return &groupMembersDataSource{}
}

type groupMembersDataSource struct {
	client *firezone.Client
}

// groupMembersDataSourceModel mirrors the firezone_group_members data
// source schema.
type groupMembersDataSourceModel struct {
	GroupID types.String       `tfsdk:"group_id"`
	Members []groupMemberModel `tfsdk:"members"`
}

// groupMemberModel is one element of firezone_group_members' members list.
type groupMemberModel struct {
	ID    types.String `tfsdk:"id"`
	Name  types.String `tfsdk:"name"`
	Email types.String `tfsdk:"email"`
	Type  types.String `tfsdk:"type"`
}

func (d *groupMembersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_members"
}

func (d *groupMembersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the Actors that are members of a Group. For the managed Everyone " +
			"Group, this is every user and admin Actor; service accounts and API clients " +
			"are not members.",
		Attributes: map[string]schema.Attribute{
			"group_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the Group to list.",
			},
			"members": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The Group's members.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "Actor ID.",
						},
						"name": schema.StringAttribute{
							Computed:    true,
							Description: "Actor name.",
						},
						"email": schema.StringAttribute{
							Computed:    true,
							Description: "The Actor's email address, or null if it has none.",
						},
						"type": schema.StringAttribute{
							Computed:    true,
							Description: "One of account_user, account_admin_user, service_account, api_client.",
						},
					},
				},
			},
		},
	}
}

func (d *groupMembersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *groupMembersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config groupMembersDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	members, err := listGroupMembers(ctx, d.client, config.GroupID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error Listing Group Members", err.Error())
		return
	}

	config.Members = make([]groupMemberModel, len(members))
	for i, member := range members {
		email := types.StringNull()
		if member.Email != "" {
			email = types.StringValue(member.Email)
		}
		config.Members[i] = groupMemberModel{
			ID:    types.StringValue(member.ID),
			Name:  types.StringValue(member.Name),
			Email: email,
			Type:  types.StringValue(string(member.Type)),
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// listGroupMembers returns every member of the Group, following pages.
func listGroupMembers(ctx context.Context, client *firezone.Client, groupID string) ([]firezone.GroupMember, error) {
	var members []firezone.GroupMember

	opts := &firezone.ListOptions{Limit: 100}
	for {
		page, err := client.Groups.Memberships(groupID).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		members = append(members, page.Data...)
		if page.Metadata.NextPage == "" {
			return members, nil
		}
		opts.PageCursor = page.Metadata.NextPage
	}
}
