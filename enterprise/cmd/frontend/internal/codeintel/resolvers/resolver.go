package resolvers

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/opentracing/opentracing-go/log"
	"github.com/pkg/errors"

	gql "github.com/sourcegraph/sourcegraph/cmd/frontend/graphqlbackend"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/autoindex/config"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/gitserver"
	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

// Resolver is the main interface to code intel-related operations exposed to the GraphQL API.
// This resolver consolidates the logic for code intel operations and is not itself concerned
// with GraphQL/API specifics (auth, validation, marshaling, etc.). This resolver is wrapped
// by a symmetrics resolver in this package's graphql subpackage, which is exposed directly
// by the API.
type Resolver interface {
	GetUploadByID(ctx context.Context, id int) (store.Upload, bool, error)
	GetIndexByID(ctx context.Context, id int) (store.Index, bool, error)
	UploadConnectionResolver(opts store.GetUploadsOptions) *UploadsResolver
	IndexConnectionResolver(opts store.GetIndexesOptions) *IndexesResolver
	DeleteUploadByID(ctx context.Context, uploadID int) error
	DeleteIndexByID(ctx context.Context, id int) error
	IndexConfiguration(ctx context.Context, repositoryID int) ([]byte, error)
	UpdateIndexConfigurationByRepositoryID(ctx context.Context, repositoryID int, configuration string) error
	CommitGraph(ctx context.Context, repositoryID int) (gql.CodeIntelligenceCommitGraphResolver, error)
	QueueAutoIndexJobForRepo(ctx context.Context, repositoryID int) error
	QueryResolver(ctx context.Context, args *gql.GitBlobLSIFDataArgs) (QueryResolver, error)
}

type resolver struct {
	dbStore         DBStore
	lsifStore       LSIFStore
	gitserverClient GitserverClient
	indexEnqueuer   IndexEnqueuer
	hunkCache       HunkCache
	operations      *operations
}

// NewResolver creates a new resolver with the given services.
func NewResolver(
	dbStore DBStore,
	lsifStore LSIFStore,
	gitserverClient GitserverClient,
	indexEnqueuer IndexEnqueuer,
	hunkCache HunkCache,
	observationContext *observation.Context,
) Resolver {
	return &resolver{
		dbStore:         dbStore,
		lsifStore:       lsifStore,
		gitserverClient: gitserverClient,
		indexEnqueuer:   indexEnqueuer,
		hunkCache:       hunkCache,
		operations:      newOperations(observationContext),
	}
}

func (r *resolver) GetUploadByID(ctx context.Context, id int) (store.Upload, bool, error) {
	return r.dbStore.GetUploadByID(ctx, id)
}

func (r *resolver) GetIndexByID(ctx context.Context, id int) (store.Index, bool, error) {
	return r.dbStore.GetIndexByID(ctx, id)
}

func (r *resolver) UploadConnectionResolver(opts store.GetUploadsOptions) *UploadsResolver {
	return NewUploadsResolver(r.dbStore, opts)
}

func (r *resolver) IndexConnectionResolver(opts store.GetIndexesOptions) *IndexesResolver {
	return NewIndexesResolver(r.dbStore, opts)
}

func (r *resolver) DeleteUploadByID(ctx context.Context, uploadID int) error {
	_, err := r.dbStore.DeleteUploadByID(ctx, uploadID)
	return err
}

func (r *resolver) DeleteIndexByID(ctx context.Context, id int) error {
	_, err := r.dbStore.DeleteIndexByID(ctx, id)
	return err
}

func (r *resolver) IndexConfiguration(ctx context.Context, repositoryID int) ([]byte, error) {
	configuration, exists, err := r.dbStore.GetIndexConfigurationByRepositoryID(ctx, repositoryID)
	if err != nil {
		return nil, err
	}

	if exists {
		return configuration.Data, nil
	}

	// nothing in DB, prepopulate with a best guess from the inference engine
	maybeConfig, err := r.indexEnqueuer.InferIndexConfiguration(ctx, repositoryID)
	if err != nil || maybeConfig == nil {
		return nil, err
	}

	marshaled, err := config.MarshalJSON(*maybeConfig)
	if err != nil {
		return nil, err
	}

	var indented bytes.Buffer
	json.Indent(&indented, marshaled, "", "\t")
	return indented.Bytes(), nil
}

func (r *resolver) UpdateIndexConfigurationByRepositoryID(ctx context.Context, repositoryID int, configuration string) error {
	if _, err := config.UnmarshalJSON([]byte(configuration)); err != nil {
		return err
	}

	return r.dbStore.UpdateIndexConfigurationByRepositoryID(ctx, repositoryID, []byte(configuration))
}

func (r *resolver) CommitGraph(ctx context.Context, repositoryID int) (gql.CodeIntelligenceCommitGraphResolver, error) {
	stale, updatedAt, err := r.dbStore.CommitGraphMetadata(ctx, repositoryID)
	if err != nil {
		return nil, err
	}

	return NewCommitGraphResolver(stale, updatedAt), nil
}

func (r *resolver) QueueAutoIndexJobForRepo(ctx context.Context, repositoryID int) error {
	return r.indexEnqueuer.ForceQueueIndex(ctx, repositoryID)
}

const slowQueryResolverRequestThreshold = time.Second

// QueryResolver determines the set of dumps that can answer code intel queries for the
// given repository, commit, and path, then constructs a new query resolver instance which
// can be used to answer subsequent queries.
func (r *resolver) QueryResolver(ctx context.Context, args *gql.GitBlobLSIFDataArgs) (_ QueryResolver, err error) {
	ctx, endObservation := observeResolver(ctx, &err, "QueryResolver", r.operations.queryResolver, slowQueryResolverRequestThreshold, observation.Args{
		LogFields: []log.Field{
			log.Int("repositoryID", int(args.Repo.ID)),
			log.String("commit", string(args.Commit)),
			log.String("path", args.Path),
			log.Bool("exactPath", args.ExactPath),
			log.String("toolName", args.ToolName),
		},
	})
	defer endObservation()

	cachedCommitChecker := newCachedCommitChecker(r.gitserverClient)
	cachedCommitChecker.Set(int(args.Repo.ID), string(args.Commit))

	dumps, err := r.findClosestDumps(
		ctx,
		cachedCommitChecker,
		int(args.Repo.ID),
		string(args.Commit),
		args.Path,
		args.ExactPath,
		args.ToolName,
	)
	if err != nil || len(dumps) == 0 {
		return nil, err
	}

	return NewQueryResolver(
		r.dbStore,
		r.lsifStore,
		cachedCommitChecker,
		NewPositionAdjuster(args.Repo, string(args.Commit), r.hunkCache),
		int(args.Repo.ID),
		string(args.Commit),
		args.Path,
		dumps,
		r.operations,
	), nil
}

// numAncestors is the number of ancestors to query from gitserver when trying to find the closest
// ancestor we have data for. Setting this value too low (relative to a repository's commit rate)
// will cause requests for an unknown commit return too few results; setting this value too high
// will raise the latency of requests for an unknown commit.
//
// TODO(efritz) - make adjustable via site configuration
const numAncestors = 100

// findClosestDumps returns the set of dumps that can most accurately answer code intelligence
// queries for the given path. If exactPath is true, then only dumps that definitely contain the
// exact document path are returned. Otherwise, dumps containing any document for which the given
// path is a prefix are returned. These dump IDs should be subsequently passed to invocations of
// Definitions, References, and Hover.
func (r *resolver) findClosestDumps(ctx context.Context, cachedCommitChecker *cachedCommitChecker, repositoryID int, commit, path string, exactPath bool, indexer string) (_ []store.Dump, err error) {
	candidates, err := r.inferClosestUploads(ctx, repositoryID, commit, path, exactPath, indexer)
	if err != nil {
		return nil, err
	}

	var dumps []store.Dump
	for _, dump := range candidates {
		exists, err := cachedCommitChecker.Exists(ctx, dump.RepositoryID, dump.Commit)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}

		if exactPath {
			pathExists, err := r.lsifStore.Exists(ctx, dump.ID, strings.TrimPrefix(path, dump.Root))
			if err != nil {
				// TODO - tag errors
				return nil, err
			}
			if !pathExists {
				continue
			}
		} else {
			// TODO(efritz) - ensure there's a valid document path for this condition as well
		}

		dumps = append(dumps, dump)
	}

	return dumps, nil
}

// inferClosestUploads will return the set of visible uploads for the given commit. If this commit is
// newer than our last refresh of the lsif_nearest_uploads table for this repository, then we will mark
// the repository as dirty and quickly approximate the correct set of visible uploads.
//
// Because updating the entire commit graph is a blocking, expensive, and lock-guarded process, we  want
// to only do that in the background and do something chearp in latency-sensitive paths. To construct an
// approximate result, we query gitserver for a (relatively small) set of ancestors for the given commit,
// correlate that with the upload data we have for those commits, and re-run the visibility algorithm over
// the graph. This will not always produce the full set of visible commits - some responses may not contain
// all results while a subsequent request made after the lsif_nearest_uploads has been updated to include
// this commit will.
//
// TODO(efritz) - show an indication in the GraphQL response and the UI that this repo is refreshing.
func (r *resolver) inferClosestUploads(ctx context.Context, repositoryID int, commit, path string, exactPath bool, indexer string) ([]store.Dump, error) {
	commitExists, err := r.dbStore.HasCommit(ctx, repositoryID, commit)
	if err != nil {
		return nil, errors.Wrap(err, "store.HasCommit")
	}
	if commitExists {
		// The parameters exactPath and rootMustEnclosePath align here: if we're looking for dumps
		// that can answer queries for a directory (e.g. diagnostics), we want any dump that happens
		// to intersect the target directory. If we're looking for dumps that can answer queries for
		// a single file, then we need a dump with a root that properly encloses that file.
		dumps, err := r.dbStore.FindClosestDumps(ctx, repositoryID, commit, path, exactPath, indexer)
		if err != nil {
			return nil, errors.Wrap(err, "store.FindClosestDumps")
		}

		return dumps, nil
	}

	repositoryExists, err := r.dbStore.HasRepository(ctx, repositoryID)
	if err != nil {
		return nil, errors.Wrap(err, "store.HasRepository")
	}
	if !repositoryExists {
		// TODO(efritz) - differentiate this error in the GraphQL response/UI
		return nil, nil
	}

	graph, err := r.gitserverClient.CommitGraph(ctx, repositoryID, gitserver.CommitGraphOptions{
		Commit: commit,
		Limit:  numAncestors,
	})
	if err != nil {
		return nil, err
	}

	dumps, err := r.dbStore.FindClosestDumpsFromGraphFragment(ctx, repositoryID, commit, path, exactPath, indexer, graph)
	if err != nil {
		return nil, err
	}

	if err := r.dbStore.MarkRepositoryAsDirty(ctx, repositoryID); err != nil {
		return nil, errors.Wrap(err, "store.MarkRepositoryAsDirty")
	}

	return dumps, nil
}
