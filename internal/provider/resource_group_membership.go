package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	firezone "github.com/firezone/firezone-go"
)

var (
	_ resource.Resource                = &groupMembershipResource{}
	_ resource.ResourceWithImportState = &groupMembershipResource{}
	_ resource.ResourceWithConfigure   = &groupMembershipResource{}
)

// NewGroupMembershipResource returns a new firezone_group_membership
// resource instance, for use with FirezoneProvider.Resources.
//
// This is a separate resource, not a "members" attribute folded onto
// firezone_group, matching the API's own independent
// /groups/{id}/memberships modeling. This lets more than one Terraform
// config manage membership on the same Group independently - e.g. one
// config owns the Group, another grants a service actor access.
//
// Create/Delete use the PATCH add/remove endpoint, never the PUT
// replace-all endpoint: using PUT per-resource would make every
// membership resource except the last-applied one silently disappear
// on the next apply. Do not "simplify" this to PUT.
func NewGroupMembershipResource() resource.Resource {
	return &groupMembershipResource{}
}

type groupMembershipResource struct {
	client *firezone.Client
}

// groupMembershipResourceModel mirrors the firezone_group_membership
// resource schema.
type groupMembershipResourceModel struct {
	ID      types.String `tfsdk:"id"`
	GroupID types.String `tfsdk:"group_id"`
	ActorID types.String `tfsdk:"actor_id"`
}

func (r *groupMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_membership"
}

func (r *groupMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Grants a single Actor membership in a Group. Multiple firezone_group_membership resources for the same Group compose additively - unlike replacing the Group's entire membership list, adding or removing one doesn't disturb any other. To put many Actors in one Group, for_each over their IDs to generate one firezone_group_membership per Actor rather than writing a block per Actor by hand - see the example below.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Composite ID in the form \"{group_id}/{actor_id}\" - the API has no dedicated membership ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"group_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the Group.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"actor_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the Actor to add to the Group.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *groupMembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	r.client = client
}

func (r *groupMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := plan.GroupID.ValueString()
	actorID := plan.ActorID.ValueString()

	err := serializedPatch(ctx, groupID, func() error {
		_, err := r.client.Groups.Memberships(groupID).Patch(ctx, []string{actorID}, nil)
		return err
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Creating Group Membership", err.Error())
		return
	}

	plan.ID = types.StringValue(membershipID(groupID, actorID))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.GroupID.ValueString()
	actorID := state.ActorID.ValueString()

	found, err := membershipExists(ctx, r.client, groupID, actorID)
	if err != nil {
		if firezone.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error Reading Group Membership", err.Error())
		return
	}
	if !found {
		// The actor was removed from the group out-of-band.
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable in practice: both group_id and actor_id are
// RequiresReplace, so any change to either triggers a replace, not an
// update. Implemented anyway because resource.Resource requires it.
func (r *groupMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan groupMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.GroupID.ValueString()
	actorID := state.ActorID.ValueString()

	err := serializedPatch(ctx, groupID, func() error {
		_, err := r.client.Groups.Memberships(groupID).Patch(ctx, nil, []string{actorID})
		return err
	})
	if err != nil && !firezone.IsNotFound(err) {
		resp.Diagnostics.AddError("Error Deleting Group Membership", err.Error())
	}
}

func (r *groupMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	groupID, actorID, err := parseMembershipID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, groupIDPath, groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, actorIDPath, actorID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, idPath, req.ID)...)
}

func membershipID(groupID, actorID string) string {
	return groupID + "/" + actorID
}

func parseMembershipID(id string) (groupID, actorID string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected import ID in the form \"group_id/actor_id\", got: %q", id)
	}
	return parts[0], parts[1], nil
}

// membershipExists paginates through the Group's members looking for
// actorID. The memberships list endpoint has no filter-by-actor-id
// query parameter, so this is a full scan - acceptable for a resource
// operated on a handful of times per apply, not a hot path.
func membershipExists(ctx context.Context, client *firezone.Client, groupID, actorID string) (bool, error) {
	opts := &firezone.ListOptions{Limit: 100}
	for {
		page, err := client.Groups.Memberships(groupID).List(ctx, opts)
		if err != nil {
			return false, err
		}
		for _, member := range page.Data {
			if member.ID == actorID {
				return true, nil
			}
		}
		if page.Metadata.NextPage == "" {
			return false, nil
		}
		opts.PageCursor = page.Metadata.NextPage
	}
}
