package graphqlbackend

import (
	"context"
	"path"
	"sync"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/relay"
	"github.com/pkg/errors"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/backend"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/db"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/graphqlbackend/graphqlutil"
	"github.com/sourcegraph/sourcegraph/pkg/a8n"
)

func (r *schemaResolver) CreateCampaign(ctx context.Context, args *struct {
	Input struct {
		Namespace   graphql.ID
		Name        string
		Description string
	}
}) (*campaignResolver, error) {
	user, err := db.Users.GetByCurrentAuthUser(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "%v", backend.ErrNotAuthenticated)
	}

	// 🚨 SECURITY: Only site admins may create a campaign for now.
	if !user.SiteAdmin {
		return nil, backend.ErrMustBeSiteAdmin
	}

	campaign := &a8n.Campaign{
		Name:        args.Input.Name,
		Description: args.Input.Description,
		AuthorID:    user.ID,
	}

	node, err := NodeByID(ctx, args.Input.Namespace)
	if err != nil {
		return nil, err
	}

	switch ns := node.(type) {
	case *UserResolver:
		campaign.NamespaceUserID = ns.DatabaseID()
	case *OrgResolver:
		campaign.NamespaceOrgID = ns.OrgID()
	default:
		return nil, errors.Errorf("Invalid namespace of type %T", ns)
	}

	if err := r.A8NStore.CreateCampaign(ctx, campaign); err != nil {
		return nil, err
	}

	return &campaignResolver{Campaign: campaign}, nil
}

func (r *schemaResolver) Campaigns(ctx context.Context, args *struct {
	graphqlutil.ConnectionArgs
}) (*campaignsConnectionResolver, error) {
	// 🚨 SECURITY: Only site admins may read external services (they have secrets).
	if err := backend.CheckCurrentUserIsSiteAdmin(ctx); err != nil {
		return nil, err
	}

	return &campaignsConnectionResolver{
		store: r.A8NStore,
		opts: a8n.ListCampaignsOpts{
			Limit: int(args.ConnectionArgs.GetFirst()),
		},
	}, nil
}

type campaignsConnectionResolver struct {
	store *a8n.Store
	opts  a8n.ListCampaignsOpts

	// cache results because they are used by multiple fields
	once      sync.Once
	campaigns []*a8n.Campaign
	next      int64
	err       error
}

func (r *campaignsConnectionResolver) Nodes(ctx context.Context) ([]*campaignResolver, error) {
	campaigns, _, err := r.compute(ctx)
	if err != nil {
		return nil, err
	}
	resolvers := make([]*campaignResolver, 0, len(campaigns))
	for _, c := range campaigns {
		resolvers = append(resolvers, &campaignResolver{Campaign: c})
	}
	return resolvers, nil
}

func (r *campaignsConnectionResolver) TotalCount(ctx context.Context) (int32, error) {
	opts := a8n.CountCampaignsOpts{ThreadID: r.opts.ThreadID}
	count, err := r.store.CountCampaigns(ctx, opts)
	return int32(count), err
}

func (r *campaignsConnectionResolver) PageInfo(ctx context.Context) (*graphqlutil.PageInfo, error) {
	_, next, err := r.compute(ctx)
	if err != nil {
		return nil, err
	}
	return graphqlutil.HasNextPage(next != 0), nil
}

func (r *campaignsConnectionResolver) compute(ctx context.Context) ([]*a8n.Campaign, int64, error) {
	r.once.Do(func() {
		r.campaigns, r.next, r.err = r.store.ListCampaigns(ctx, r.opts)
	})
	return r.campaigns, r.next, r.err
}

type campaignResolver struct{ *a8n.Campaign }

const campaignIDKind = "Campaign"

func marshalCampaignID(id int64) graphql.ID {
	return relay.MarshalID(campaignIDKind, id)
}

func unmarshalCampaignID(id graphql.ID) (campaignID int64, err error) {
	err = relay.UnmarshalSpec(id, &campaignID)
	return
}

func (r *campaignResolver) ID() graphql.ID {
	return marshalCampaignID(r.Campaign.ID)
}

func (r *campaignResolver) Name() string {
	return r.Campaign.Name
}

func (r *campaignResolver) Description() string {
	return r.Campaign.Description
}

func (r *campaignResolver) Author(ctx context.Context) (*UserResolver, error) {
	return UserByIDInt32(ctx, r.AuthorID)
}

func (r *campaignResolver) URL(ctx context.Context) (string, error) {
	// TODO(tsenart): Query for namespace only once
	ns, err := r.Namespace(ctx)
	if err != nil {
		return "", err
	}
	return path.Join(ns.URL(), "campaigns", string(r.ID())), nil
}

func (r *campaignResolver) Namespace(ctx context.Context) (n namespaceResolver, err error) {
	if r.NamespaceUserID != 0 {
		n.Namespace, err = UserByIDInt32(ctx, r.NamespaceUserID)
	} else {
		n.Namespace, err = OrgByIDInt32(ctx, r.NamespaceOrgID)
	}

	return n, err
}

func (r *campaignResolver) CreatedAt() DateTime {
	return DateTime{Time: r.Campaign.CreatedAt}
}

func (r *campaignResolver) UpdatedAt() DateTime {
	return DateTime{Time: r.Campaign.UpdatedAt}
}
