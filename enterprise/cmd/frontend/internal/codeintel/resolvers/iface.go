package resolvers

import (
	"context"
	"time"

	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/autoindex/config"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/gitserver"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
)

type GitserverClient interface {
	CommitExists(ctx context.Context, repositoryID int, commit string) (bool, error)
	CommitGraph(ctx context.Context, repositoryID int, options gitserver.CommitGraphOptions) (*gitserver.CommitGraph, error)
}

type DBStore interface {
	gitserver.DBStore

	GetUploadByID(ctx context.Context, id int) (dbstore.Upload, bool, error)
	GetUploads(ctx context.Context, opts dbstore.GetUploadsOptions) ([]dbstore.Upload, int, error)
	DeleteUploadByID(ctx context.Context, id int) (bool, error)
	GetDumpByID(ctx context.Context, id int) (dbstore.Dump, bool, error)
	GetDumpByIDs(ctx context.Context, ids []int) ([]dbstore.Dump, error)
	FindClosestDumps(ctx context.Context, repositoryID int, commit, path string, rootMustEnclosePath bool, indexer string) ([]dbstore.Dump, error)
	FindClosestDumpsFromGraphFragment(ctx context.Context, repositoryID int, commit, path string, rootMustEnclosePath bool, indexer string, graph *gitserver.CommitGraph) ([]dbstore.Dump, error)
	PackageIDs(ctx context.Context, monikers []lsifstore.QualifiedMonikerData) (_ []int, err error)
	ReferenceIDsAndFilters(ctx context.Context, repositoryID int, commit string, monikers []lsifstore.QualifiedMonikerData) (_ map[int][][]byte, err error)
	HasRepository(ctx context.Context, repositoryID int) (bool, error)
	HasCommit(ctx context.Context, repositoryID int, commit string) (bool, error)
	MarkRepositoryAsDirty(ctx context.Context, repositoryID int) error
	CommitGraphMetadata(ctx context.Context, repositoryID int) (stale bool, updatedAt *time.Time, _ error)
	GetIndexByID(ctx context.Context, id int) (dbstore.Index, bool, error)
	GetIndexes(ctx context.Context, opts dbstore.GetIndexesOptions) ([]dbstore.Index, int, error)
	DeleteIndexByID(ctx context.Context, id int) (bool, error)
	GetIndexConfigurationByRepositoryID(ctx context.Context, repositoryID int) (store.IndexConfiguration, bool, error)
	UpdateIndexConfigurationByRepositoryID(ctx context.Context, repositoryID int, data []byte) error
}

type LSIFStore interface {
	Exists(ctx context.Context, bundleID int, path string) (bool, error)
	Ranges(ctx context.Context, bundleID int, path string, startLine, endLine int) ([]lsifstore.CodeIntelligenceRange, error)
	Definitions(ctx context.Context, bundleID int, path string, line, character int) ([]lsifstore.Location, error)
	References(ctx context.Context, bundleID int, path string, line, character int) ([]lsifstore.Location, error)
	Hover(ctx context.Context, bundleID int, path string, line, character int) (string, lsifstore.Range, bool, error)
	Diagnostics(ctx context.Context, bundleID int, prefix string, skip, take int) ([]lsifstore.Diagnostic, int, error)
	MonikersByPosition(ctx context.Context, bundleID int, path string, line, character int) ([][]lsifstore.MonikerData, error)
	MonikerResults(ctx context.Context, bundleID int, tableName, scheme, identifier string, skip, take int) ([]lsifstore.Location, int, error)
	PackageInformation(ctx context.Context, bundleID int, path string, packageInformationID string) (lsifstore.PackageInformationData, bool, error)
	BulkMonikerResults(ctx context.Context, tableName string, ids []int, args []lsifstore.MonikerData, skip, take int) (_ []lsifstore.Location, _ int, err error)
}

type IndexEnqueuer interface {
	ForceQueueIndex(ctx context.Context, repositoryID int) error
	InferIndexConfiguration(ctx context.Context, repositoryID int) (*config.IndexConfiguration, error)
}
