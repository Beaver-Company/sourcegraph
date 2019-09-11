package graphqlbackend

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/errors"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/backend"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/db"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/types"
	"github.com/sourcegraph/sourcegraph/cmd/repo-updater/repos"
	"github.com/sourcegraph/sourcegraph/pkg/a8n"
	"github.com/sourcegraph/sourcegraph/pkg/actor"
	"github.com/sourcegraph/sourcegraph/pkg/api"
	"github.com/sourcegraph/sourcegraph/pkg/conf"
	"github.com/sourcegraph/sourcegraph/pkg/db/dbconn"
	"github.com/sourcegraph/sourcegraph/pkg/db/dbtesting"
	"github.com/sourcegraph/sourcegraph/pkg/jsonc"
)

func TestCampaigns(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	ctx := backend.WithAuthzBypass(context.Background())
	dbtesting.SetupGlobalTestDB(t)

	sr := schemaResolver{
		A8NStore: a8n.NewStore(dbconn.Global),
	}

	schema, err := graphql.ParseSchema(Schema, &sr)
	if err != nil {
		t.Fatal(err)
	}

	type User struct {
		ID         string
		DatabaseID int32
		SiteAdmin  bool
	}

	var users struct {
		Admin, User struct {
			User `json:"user"`
		}
	}

	mustExec(ctx, t, schema, nil, &users, `
		fragment u on User { id, databaseID, siteAdmin }
		mutation {
			admin: createUser(username: "admin") {
				user { ...u }
			}
			user: createUser(username: "user") {
				user { ...u }
			}
		}
	`)

	if !users.Admin.SiteAdmin {
		t.Fatal("admin must be a site-admin, since it was the first user created")
	}

	type Org struct {
		ID   string
		Name string
	}

	var orgs struct {
		ACME Org
	}

	ctx = actor.WithActor(ctx, actor.FromUser(users.Admin.DatabaseID))
	mustExec(ctx, t, schema, nil, &orgs, `
		fragment o on Org { id, name }
		mutation {
			acme: createOrganization(name: "ACME") { ...o }
		}
	`)

	type UserOrg struct {
		ID         string
		DatabaseID int32
		SiteAdmin  bool
		Name       string
	}

	type Campaign struct {
		ID          string
		Name        string
		Description string
		Author      User
		CreatedAt   string
		UpdatedAt   string
		Namespace   UserOrg
	}

	var campaigns struct{ Admin, Org Campaign }

	input := map[string]interface{}{
		"admin": map[string]interface{}{
			"namespace":   users.Admin.ID,
			"name":        "Admin Campaign",
			"description": "It's an admin's campaign",
		},
		"org": map[string]interface{}{
			"namespace":   orgs.ACME.ID,
			"name":        "ACME's Campaign",
			"description": "It's an ACME's campaign",
		},
	}

	mustExec(ctx, t, schema, input, &campaigns, `
		fragment u on User { id, databaseID, siteAdmin }
		fragment o on Org  { id, name }
		fragment c on Campaign {
			id, name, description, createdAt, updatedAt
			author    { ...u }
			namespace {
				... on User { ...u }
				... on Org  { ...o }
			}
		}
		mutation($admin: CreateCampaignInput!, $org: CreateCampaignInput!){
			admin: createCampaign(input: $admin) { ...c }
			org: createCampaign(input: $org)     { ...c }
		}
	`)

	if have, want := campaigns.Admin.Namespace.ID, users.Admin.ID; have != want {
		t.Fatalf("have admin's campaign namespace id %q, want %q", have, want)
	}

	if have, want := campaigns.Org.Namespace.ID, orgs.ACME.ID; have != want {
		t.Fatalf("have orgs's campaign namespace id %q, want %q", have, want)
	}

	type CampaignConnection struct {
		Nodes      []Campaign
		TotalCount int
		PageInfo   struct {
			HasNextPage bool
		}
	}

	var listed struct {
		First, All CampaignConnection
	}

	mustExec(ctx, t, schema, nil, &listed, `
		fragment u on User { id, databaseID, siteAdmin }
		fragment o on Org  { id, name }
		fragment c on Campaign {
			id, name, description, createdAt, updatedAt
			author    { ...u }
			namespace {
				... on User { ...u }
				... on Org  { ...o }
			}
		}
		fragment n on CampaignConnection {
			nodes { ...c }
			totalCount
			pageInfo { hasNextPage }
		}
		query {
			first: campaigns(first: 1) { ...n }
			all: campaigns() { ...n }
		}
	`)

	have := listed.First.Nodes
	want := []Campaign{campaigns.Admin}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("wrong campaigns listed. diff=%s", cmp.Diff(have, want))
	}

	if !listed.First.PageInfo.HasNextPage {
		t.Errorf("wrong page info: %+v", listed.First.PageInfo.HasNextPage)
	}

	have = listed.All.Nodes
	want = []Campaign{campaigns.Admin, campaigns.Org}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("wrong campaigns listed. diff=%s", cmp.Diff(have, want))
	}

	if listed.All.PageInfo.HasNextPage {
		t.Errorf("wrong page info: %+v", listed.All.PageInfo.HasNextPage)
	}

	repoStore := repos.NewDBStore(dbconn.Global, sql.TxOptions{
		Isolation: sql.LevelSerializable,
	})
	externalService := &types.ExternalService{
		Kind:        "GITHUB",
		DisplayName: "GitHub",
		Config: `{
		"url": "https://github.com",
		"token": "TOKEN",
		"repos": ["sourcegraph/sourcegraph"]
		}`,
	}

	err = db.ExternalServices.Create(ctx, conf.Get, externalService)
	if err != nil {
		t.Fatal(t)
	}

	urn := fmt.Sprintf("extsvc:github:%d", externalService.ID)

	repo := &repos.Repo{
		Name:        "github.com/sourcegraph/sourcegraph",
		Description: "Code search and navigation tool (self-hosted)",
		URI:         "github.com/sourcegraph/sourcegraph",
		Language:    "",
		Fork:        false,
		Enabled:     true,
		Archived:    false,
		ExternalRepo: api.ExternalRepoSpec{
			ID:          "MDEwOlJlcG9zaXRvcnk0MTI4ODcwOA==",
			ServiceType: "github",
			ServiceID:   "https://github.com",
		},
		Sources: map[string]*repos.SourceInfo{
			urn: {
				ID:       urn,
				CloneURL: "https://TOKEN@github.com/sourcegraph/sourcegraph",
			},
		},
		Metadata: "{}",
	}

	err = repoStore.UpsertRepos(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}

	input = map[string]interface{}{
		"url":      "http://github.com/sourcegraph/sourcegraph/issues/1",
		"campaign": campaigns.Admin.ID,
	}

	type Thread struct {
		ID         string
		Repository struct{ ID string }
		Campaigns  CampaignConnection
		CreatedAt  string
		UpdatedAt  string
	}

	var result struct {
		Thread Thread
	}

	mustExec(ctx, t, schema, input, &result, `
		fragment u on User { id, databaseID, siteAdmin }
		fragment o on Org  { id, name }
		fragment c on Campaign {
			id, name, description, createdAt, updatedAt
			author    { ...u }
			namespace {
				... on User { ...u }
				... on Org  { ...o }
			}
		}
		fragment n on CampaignConnection {
			nodes { ...c }
			totalCount
			pageInfo { hasNextPage }
		}
		fragment t on Thread {
			id
			repository { id }
			campaigns { ...n }
			createdAt
			updatedAt
		}
		mutation($url: String!, $campaign: ID!) {
			thread: addThreadFromURLToCampaign(url: $url, campaign: $campaign) {
				...t
			}
		}
	`)

	if result.Thread.ID == "" {
		t.Fatalf("thread id is blank")
	}
	if have, want := result.Thread.Campaigns.TotalCount, 1; have != want {
		t.Fatalf("TotalCount of campaigns for thread is %d, want %d", have, want)
	}
	if have, want := result.Thread.Campaigns.Nodes[0].ID, campaigns.Admin.ID; have != want {
		t.Fatalf("have thread campaign id %q, want %q", have, want)
	}

	wantRepoID := string(marshalRepositoryID(api.RepoID(repo.ID)))
	if have, want := result.Thread.Repository.ID, wantRepoID; have != want {
		t.Fatalf("have thread repo id %q, want %q", have, want)
	}
}

func mustExec(
	ctx context.Context,
	t testing.TB,
	s *graphql.Schema,
	in map[string]interface{},
	out interface{},
	query string,
) {
	t.Helper()
	if errs := exec(ctx, t, s, in, out, query); len(errs) > 0 {
		t.Fatalf("unexpected graphql query errors: %v", errs)
	}
}

func exec(
	ctx context.Context,
	t testing.TB,
	s *graphql.Schema,
	in map[string]interface{},
	out interface{},
	query string,
) []*errors.QueryError {
	t.Helper()

	query = strings.Replace(query, "\t", "  ", -1)

	r := s.Exec(ctx, query, "", in)
	if len(r.Errors) != 0 {
		return r.Errors
	}

	if testing.Verbose() {
		t.Logf("\n---- GraphQL Query ----\n%s\n\nVars: %s\n---- GraphQL Result ----\n%s\n -----------", query, toJSON(t, in), r.Data)
	}

	if err := json.Unmarshal(r.Data, out); err != nil {
		t.Fatalf("failed to unmarshal graphql data: %v", err)
	}

	return nil
}

func toJSON(t testing.TB, v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	formatted, err := jsonc.Format(string(data), nil)
	if err != nil {
		t.Fatal(err)
	}

	return formatted
}
