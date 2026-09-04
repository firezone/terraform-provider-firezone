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

	firezone "github.com/firezone/firezone-sdk-go"
)

var (
	_ resource.Resource                = &poolMemberResource{}
	_ resource.ResourceWithImportState = &poolMemberResource{}
	_ resource.ResourceWithConfigure   = &poolMemberResource{}
)

// NewPoolMemberResource returns a new firezone_pool_member resource
// instance, for use with FirezoneProvider.Resources.
//
// This is a separate resource, not a "device_ids" attribute folded onto
// firezone_resource, matching the API's own independent
// /resources/{id}/pool_members modeling. This lets more than one
// Terraform config manage membership on the same pool independently.
//
// Create/Delete use the PATCH add/remove endpoint, never the PUT
// replace-all endpoint: using PUT per-resource would make every pool
// member except the last-applied one silently disappear on the next
// apply. Do not "simplify" this to PUT.
func NewPoolMemberResource() resource.Resource {
	return &poolMemberResource{}
}

type poolMemberResource struct {
	client *firezone.Client
}

// poolMemberResourceModel mirrors the firezone_pool_member resource
// schema.
type poolMemberResourceModel struct {
	ID         types.String `tfsdk:"id"`
	ResourceID types.String `tfsdk:"resource_id"`
	DeviceID   types.String `tfsdk:"device_id"`
}

func (r *poolMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pool_member"
}

func (r *poolMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Adds a single Client to a static_device_pool Resource. Multiple " +
			"firezone_pool_member resources for the same pool compose additively - unlike " +
			"replacing the pool's entire membership, adding or removing one doesn't disturb " +
			"any other. To put many Clients in one pool, for_each over their IDs to generate " +
			"one firezone_pool_member per Client rather than writing a block per Client by hand.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				Description: "Composite ID in the form \"{resource_id}/{device_id}\" - the API " +
					"has no dedicated pool membership ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"resource_id": schema.StringAttribute{
				Required: true,
				Description: "ID of the static_device_pool Resource. Any other Resource type is " +
					"rejected by the API - only device pools have members.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"device_id": schema.StringAttribute{
				Required: true,
				Description: "ID of the Client to add to the pool. Must be a Client, not a " +
					"Gateway. Clients register themselves on first connect, so Terraform can " +
					"reference them but cannot create them.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *poolMemberResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := configureClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	r.client = client
}

func (r *poolMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan poolMemberResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceID := plan.ResourceID.ValueString()
	deviceID := plan.DeviceID.ValueString()

	err := serializedPatch(ctx, resourceID, func() error {
		_, err := r.client.Resources.PoolMembers(resourceID).Patch(ctx, []string{deviceID}, nil)
		return err
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Creating Pool Member", err.Error())
		return
	}

	plan.ID = types.StringValue(poolMemberID(resourceID, deviceID))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *poolMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state poolMemberResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceID := state.ResourceID.ValueString()
	deviceID := state.DeviceID.ValueString()

	found, err := poolMemberExists(ctx, r.client, resourceID, deviceID)
	if err != nil {
		if firezone.IsNotFound(err) {
			// The pool Resource itself is gone, so its membership is too.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error Reading Pool Member", err.Error())
		return
	}
	if !found {
		// The Client was removed from the pool out-of-band.
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable in practice: both resource_id and device_id are
// RequiresReplace, so any change to either triggers a replace, not an
// update. Implemented anyway because resource.Resource requires it.
func (r *poolMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan poolMemberResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *poolMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state poolMemberResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceID := state.ResourceID.ValueString()
	deviceID := state.DeviceID.ValueString()

	err := serializedPatch(ctx, resourceID, func() error {
		_, err := r.client.Resources.PoolMembers(resourceID).Patch(ctx, nil, []string{deviceID})
		return err
	})
	if err != nil && !firezone.IsNotFound(err) {
		resp.Diagnostics.AddError("Error Deleting Pool Member", err.Error())
	}
}

func (r *poolMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resourceID, deviceID, err := parsePoolMemberID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, resourceIDPath, resourceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, deviceIDPath, deviceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, idPath, req.ID)...)
}

func poolMemberID(resourceID, deviceID string) string {
	return resourceID + "/" + deviceID
}

func parsePoolMemberID(id string) (resourceID, deviceID string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected import ID in the form \"resource_id/device_id\", got: %q", id)
	}
	return parts[0], parts[1], nil
}

// poolMemberExists paginates through the pool's members looking for
// deviceID. The pool members list endpoint has no
// filter-by-device-id query parameter, so this is a full scan -
// acceptable for a resource operated on a handful of times per apply,
// not a hot path.
func poolMemberExists(ctx context.Context, client *firezone.Client, resourceID, deviceID string) (bool, error) {
	opts := &firezone.ListOptions{Limit: 100}
	for {
		page, err := client.Resources.PoolMembers(resourceID).List(ctx, opts)
		if err != nil {
			return false, err
		}
		for _, member := range page.Data {
			if member.ID == deviceID {
				return true, nil
			}
		}
		if page.Metadata.NextPage == "" {
			return false, nil
		}
		opts.PageCursor = page.Metadata.NextPage
	}
}
