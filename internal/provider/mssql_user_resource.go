package provider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/openaxon/terraform-provider-mssql/internal/core"
	"github.com/openaxon/terraform-provider-mssql/internal/mssql"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &MssqlUserResource{}
var _ resource.ResourceWithImportState = &MssqlUserResource{}

func NewMssqlUserResource() resource.Resource {
	return &MssqlUserResource{}
}

type MssqlUserResource struct {
	ctx core.ProviderData
}

type MssqlUserResourceModel struct {
	Id            types.String `tfsdk:"id"`
	Username      types.String `tfsdk:"username"`
	Password      types.String `tfsdk:"password"`
	External      types.Bool   `tfsdk:"external"`
	Login         types.String `tfsdk:"login"`
	Sid           types.String `tfsdk:"sid"`
	DefaultSchema types.String `tfsdk:"default_schema"`
}

func (r *MssqlUserResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *MssqlUserResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "MssqlUser resource",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"username": schema.StringAttribute{
				MarkdownDescription: "MssqlUser configurable attribute with default value",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"password": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				MarkdownDescription: "Password for the login. Must follow strong password policies defined for SQL server. " +
					"Passwords are case-sensitive, length must be 8-128 chars, can include all characters except `'` or `name`.\n\n" +
					"~> **Note** Password will be stored in the raw state as plain-text. [Read more about sensitive data in state](https://www.terraform.io/language/state/sensitive-data).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"external": schema.BoolAttribute{
				MarkdownDescription: "Is this an external user (like Microsoft EntraID)",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"login": schema.StringAttribute{
				MarkdownDescription: "Login to associate to this user",
				Optional:            true,
			},
			"sid": schema.StringAttribute{
				MarkdownDescription: "Set custom SID for the user",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"default_schema": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("dbo"),
			},
		},
	}
}

func (r *MssqlUserResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*core.ProviderData)

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *core.SqlClient, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)

		return
	}

	r.ctx = *client
}

func (r *MssqlUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data MssqlUserResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	create := mssql.CreateUser{
		Username:      data.Username.ValueString(),
		Password:      data.Password.ValueString(),
		Sid:           data.Sid.ValueString(),
		External:      data.External.ValueBool(),
		Login:         data.Login.ValueString(),
		DefaultSchema: data.DefaultSchema.ValueString(),
	}

	user, err := r.ctx.Client.CreateUser(ctx, create)
	if err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Error creating user %s", create.Username), err.Error())
		return
	}

	userToResource(&data, user)
	tflog.Debug(ctx, fmt.Sprintf("Created user %s", data.Username))

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func userToResource(data *MssqlUserResourceModel, user mssql.User) {
	data.Id = types.StringValue(user.Id)
	data.Username = types.StringValue(user.Username)

	if user.Sid != "" {
		data.Sid = types.StringValue(user.Sid)
	}

	// Deal with https://github.com/hashicorp/terraform-provider-kubernetes/issues/2185
	// no need to make everything "computed" if we don't have to.
	if user.Login != "" {
		data.Login = types.StringValue(user.Login)
	}

	data.External = types.BoolValue(user.External)
	data.DefaultSchema = types.StringValue(user.DefaultSchema)
}

func (r *MssqlUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data MssqlUserResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	user, err := r.ctx.Client.GetUser(ctx, data.Id.ValueString())

	// If resource is not found, remove it from the state
	if errors.Is(err, sql.ErrNoRows) {
		resp.State.RemoveResource(ctx)
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Unable", fmt.Sprintf("Unable to read MssqlUser, got error: %s", err))
		return
	}

	userToResource(&data, user)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *MssqlUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data MssqlUserResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	user := mssql.UpdateUser{
		Id:            data.Id.ValueString(),
		Password:      data.Password.ValueString(),
		Login:         data.Login.ValueString(),
		DefaultSchema: data.DefaultSchema.ValueString(),
	}

	cur, err := r.ctx.Client.UpdateUser(ctx, user)
	if err != nil {
		resp.Diagnostics.AddError("could not update user", err.Error())
		return
	}

	data.Id = types.StringValue(cur.Id)
	data.DefaultSchema = types.StringValue(cur.DefaultSchema)

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *MssqlUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data MssqlUserResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.ctx.Client.DeleteUser(ctx, data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("unable to delete user", fmt.Sprintf("unable to delete user %s, got error: %s", data.Username.ValueString(), err))
		return
	}
}

func (r *MssqlUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
