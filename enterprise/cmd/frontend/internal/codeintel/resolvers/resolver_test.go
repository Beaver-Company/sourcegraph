package resolvers

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	gql "github.com/sourcegraph/sourcegraph/cmd/frontend/graphqlbackend"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/autoindex/enqueuer"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/internal/api"
	"github.com/sourcegraph/sourcegraph/internal/observation"
	"github.com/sourcegraph/sourcegraph/internal/types"
)

func TestQueryResolver(t *testing.T) {
	mockDBStore := NewMockDBStore()
	mockLSIFStore := NewMockLSIFStore()
	mockCodeIntelAPI := NewMockCodeIntelAPI() // returns no dumps

	resolver := NewResolver(mockDBStore, mockLSIFStore, mockCodeIntelAPI, nil, nil, &observation.TestContext)
	queryResolver, err := resolver.QueryResolver(context.Background(), &gql.GitBlobLSIFDataArgs{
		Repo:      &types.Repo{ID: 50},
		Commit:    api.CommitID("deadbeef"),
		Path:      "/foo/bar.go",
		ExactPath: true,
		ToolName:  "lsif-go",
	})
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	if queryResolver != nil {
		t.Errorf("expected nil-valued resolver")
	}
}

// import (
// 	"context"
// 	"strings"
// 	"testing"

// 	"github.com/google/go-cmp/cmp"

// 	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/gitserver"
// 	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
// 	"github.com/sourcegraph/sourcegraph/internal/observation"
// )

// func TestFindClosestDumps(t *testing.T) {
// 	mockDBStore := NewMockDBStore()
// 	mockLSIFStore := NewMockLSIFStore()
// 	mockGitserverClient := NewMockGitserverClient()

// 	setMockDBStoreHasRepository(t, mockDBStore, 42, true)
// 	setMockDBStoreHasCommit(t, mockDBStore, 42, testCommit, true)
// 	setMockDBStoreFindClosestDumps(t, mockDBStore, 42, testCommit, "s1/main.go", true, "idx", []store.Dump{
// 		{ID: 50, RepositoryID: 42, Commit: makeCommit(0), Root: "s1/"},
// 		{ID: 51, RepositoryID: 42, Commit: makeCommit(1), Root: "s1/"},
// 		{ID: 52, RepositoryID: 42, Commit: makeCommit(2), Root: "s1/"},
// 		{ID: 53, RepositoryID: 42, Commit: makeCommit(3), Root: "s2/"},
// 		{ID: 54, RepositoryID: 42, Commit: makeCommit(4), Root: "s3/"},
// 	})
// 	setMultimockLSIFStoreExists(
// 		t,
// 		mockLSIFStore,
// 		existsSpec{50, "main.go", true},
// 		existsSpec{51, "main.go", false},
// 		existsSpec{52, "main.go", true},
// 		existsSpec{53, "s1/main.go", false},
// 		existsSpec{54, "s1/main.go", true},
// 	)

// 	mockGitserverClient.CommitExistsFunc.SetDefaultHook(func(ctx context.Context, repositoryID int, commit string) (bool, error) {
// 		return strings.Compare(commit, makeCommit(3)) <= 0, nil
// 	})

// 	api := New(mockDBStore, mockLSIFStore, mockGitserverClient, &observation.TestContext)
// 	dumps, err := api.FindClosestDumps(context.Background(), 42, testCommit, "s1/main.go", true, "idx")
// 	if err != nil {
// 		t.Fatalf("unexpected error finding closest dumps: %s", err)
// 	}

// 	expected := []store.Dump{
// 		{ID: 50, RepositoryID: 42, Commit: makeCommit(0), Root: "s1/"},
// 		{ID: 52, RepositoryID: 42, Commit: makeCommit(2), Root: "s1/"},
// 	}
// 	if diff := cmp.Diff(expected, dumps); diff != "" {
// 		t.Errorf("unexpected dumps (-want +got):\n%s", diff)
// 	}
// }

// func TestFindClosestDumpsInfersClosestUploads(t *testing.T) {
// 	mockDBStore := NewMockDBStore()
// 	mockLSIFStore := NewMockLSIFStore()
// 	mockGitserverClient := NewMockGitserverClient()
// 	mockGitserverClient.CommitExistsFunc.SetDefaultReturn(true, nil)

// 	graph := gitserver.ParseCommitGraph([]string{
// 		"d",
// 		"c",
// 		"b d",
// 		"a b c",
// 	})
// 	expectedGraph := map[string][]string{
// 		"a": {"b", "c"},
// 		"b": {"d"},
// 		"c": {},
// 		"d": {},
// 	}

// 	setMockDBStoreHasRepository(t, mockDBStore, 42, true)
// 	setMockDBStoreHasCommit(t, mockDBStore, 42, testCommit, false)
// 	setMockGitserverCommitGraph(t, mockGitserverClient, 42, graph)
// 	setMockDBStoreFindClosestDumpsFromGraphFragment(t, mockDBStore, 42, testCommit, "s1/main.go", true, "idx", expectedGraph, []store.Dump{
// 		{ID: 50, Root: "s1/"},
// 		{ID: 51, Root: "s1/"},
// 		{ID: 52, Root: "s1/"},
// 		{ID: 53, Root: "s2/"},
// 	})
// 	setMultimockLSIFStoreExists(
// 		t,
// 		mockLSIFStore,
// 		existsSpec{50, "main.go", true},
// 		existsSpec{51, "main.go", false},
// 		existsSpec{52, "main.go", true},
// 		existsSpec{53, "s1/main.go", false},
// 	)

// 	api := New(mockDBStore, mockLSIFStore, mockGitserverClient, &observation.TestContext)
// 	dumps, err := api.FindClosestDumps(context.Background(), 42, testCommit, "s1/main.go", true, "idx")
// 	if err != nil {
// 		t.Fatalf("unexpected error finding closest dumps: %s", err)
// 	}

// 	expected := []store.Dump{
// 		{ID: 50, Root: "s1/"},
// 		{ID: 52, Root: "s1/"},
// 	}
// 	if diff := cmp.Diff(expected, dumps); diff != "" {
// 		t.Errorf("unexpected dumps (-want +got):\n%s", diff)
// 	}

// 	if value := len(mockDBStore.MarkRepositoryAsDirtyFunc.History()); value != 1 {
// 		t.Errorf("expected number of calls to store.MarkRepositoryAsDirty. want=%d have=%d", 1, value)
// 	}
// }

// func TestFindClosestDumpsDoesNotInferClosestUploadForUnknownRepository(t *testing.T) {
// 	mockDBStore := NewMockDBStore()
// 	mockLSIFStore := NewMockLSIFStore()
// 	mockGitserverClient := NewMockGitserverClient()
// 	mockGitserverClient.CommitExistsFunc.SetDefaultReturn(true, nil)

// 	setMockDBStoreHasRepository(t, mockDBStore, 42, false)
// 	setMockDBStoreHasCommit(t, mockDBStore, 42, testCommit, false)

// 	api := New(mockDBStore, mockLSIFStore, mockGitserverClient, &observation.TestContext)
// 	dumps, err := api.FindClosestDumps(context.Background(), 42, testCommit, "s1/main.go", true, "idx")
// 	if err != nil {
// 		t.Fatalf("unexpected error finding closest dumps: %s", err)
// 	}
// 	if len(dumps) != 0 {
// 		t.Errorf("unexpected number of dumps. want=%d have=%d", 0, len(dumps))
// 	}

// 	if value := len(mockDBStore.MarkRepositoryAsDirtyFunc.History()); value != 0 {
// 		t.Errorf("expected number of calls to store.MarkRepositoryAsDirty. want=%d have=%d", 0, value)
// 	}
// }

func TestFallbackIndexConfiguration(t *testing.T) {
	mockDBStore := NewMockDBStore()
	mockEnqueuerDBStore := enqueuer.NewMockDBStore()
	mockLSIFStore := NewMockLSIFStore()
	mockCodeIntelAPI := NewMockCodeIntelAPI() // returns no dumps
	gitServerClient := enqueuer.NewMockGitserverClient()
	indexEnqueuer := enqueuer.NewIndexEnqueuer(mockEnqueuerDBStore, gitServerClient, &observation.TestContext)

	resolver := NewResolver(mockDBStore, mockLSIFStore, mockCodeIntelAPI, indexEnqueuer, nil, &observation.TestContext)

	mockDBStore.GetIndexConfigurationByRepositoryIDFunc.SetDefaultReturn(dbstore.IndexConfiguration{}, false, nil)
	gitServerClient.HeadFunc.SetDefaultReturn("deadbeef", nil)
	gitServerClient.ListFilesFunc.SetDefaultReturn([]string{"go.mod"}, nil)

	json, err := resolver.IndexConfiguration(context.Background(), 0)

	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	diff := cmp.Diff(string(json), `{
	"shared_steps": [],
	"index_jobs": [
		{
			"steps": [
				{
					"root": "",
					"image": "sourcegraph/lsif-go:latest",
					"commands": [
						"go mod download"
					]
				}
			],
			"local_steps": [],
			"root": "",
			"indexer": "sourcegraph/lsif-go:latest",
			"indexer_args": [
				"lsif-go",
				"--no-animation"
			],
			"outfile": ""
		}
	]
}`)

	if diff != "" {
		t.Fatalf("Unexpected fallback index configuration:\n%s\n", diff)
	}
}
