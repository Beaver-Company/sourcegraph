package resolvers

import (
	"context"

	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
)

// TODO - redocument
// AdjustedLocation is similar to a codeintelapi.ResolvedLocation, but with fields denoting
// the commit and range adjusted for the target commit (when the requested commit is not indexed).
type AdjustedLocation struct {
	Dump           store.Dump
	Path           string
	AdjustedCommit string
	AdjustedRange  lsifstore.Range
}

// TODO - redocument
// AdjustedDiagnostic is similar to a codeintelapi.ResolvedDiagnostic, but with fields denoting
// the commit and range adjusted for the target commit (when the requested commit is not indexed).
type AdjustedDiagnostic struct {
	lsifstore.Diagnostic
	Dump           store.Dump
	AdjustedCommit string
	AdjustedRange  lsifstore.Range
}

// TODO - redocument
// AdjustedCodeIntelligenceRange is similar to a codeintelapi.CodeIntelligenceRange,
// but with adjusted definition and reference locations.
type AdjustedCodeIntelligenceRange struct {
	Range       lsifstore.Range
	Definitions []AdjustedLocation
	References  []AdjustedLocation
	HoverText   string
}

// QueryResolver is the main interface to bundle-related operations exposed to the GraphQL API. This
// resolver consolidates the logic for bundle operations and is not itself concerned with GraphQL/API
// specifics (auth, validation, marshaling, etc.). This resolver is wrapped by a symmetrics resolver
// in this package's graphql subpackage, which is exposed directly by the API.
type QueryResolver interface {
	Ranges(ctx context.Context, startLine, endLine int) ([]AdjustedCodeIntelligenceRange, error)
	Definitions(ctx context.Context, line, character int) ([]AdjustedLocation, error)
	References(ctx context.Context, line, character, limit int, rawCursor string) ([]AdjustedLocation, string, error)
	Hover(ctx context.Context, line, character int) (string, lsifstore.Range, bool, error)
	Diagnostics(ctx context.Context, limit int) ([]AdjustedDiagnostic, int, error)
}

type queryResolver struct {
	dbStore             DBStore
	lsifStore           LSIFStore
	cachedCommitChecker *cachedCommitChecker
	positionAdjuster    PositionAdjuster
	repositoryID        int
	commit              string
	path                string
	uploads             []store.Dump
	operations          *operations
}

// NewQueryResolver create a new query resolver with the given services. The methods of this
// struct return queries for the given repository, commit, and path, and will query only the
// bundles associated with the given dump objects.
func NewQueryResolver(
	dbStore DBStore,
	lsifStore LSIFStore,
	cachedCommitChecker *cachedCommitChecker,
	positionAdjuster PositionAdjuster,
	repositoryID int,
	commit string,
	path string,
	uploads []store.Dump,
	operations *operations,
) QueryResolver {
	return &queryResolver{
		dbStore:             dbStore,
		lsifStore:           lsifStore,
		cachedCommitChecker: cachedCommitChecker,
		positionAdjuster:    positionAdjuster,
		operations:          operations,
		repositoryID:        repositoryID,
		commit:              commit,
		path:                path,
		uploads:             uploads,
	}
}
