package resolvers

import (
	"context"
	"strconv"

	codeintelapi "github.com/sourcegraph/sourcegraph/enterprise/cmd/frontend/internal/codeintel/api"
	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
)

// AdjustedLocation is similar to a codeintelapi.ResolvedLocation, but with fields denoting
// the commit and range adjusted for the target commit (when the requested commit is not indexed).
type AdjustedLocation struct {
	Dump           store.Dump
	Path           string
	AdjustedCommit string
	AdjustedRange  lsifstore.Range
}

// AdjustedDiagnostic is similar to a codeintelapi.ResolvedDiagnostic, but with fields denoting
// the commit and range adjusted for the target commit (when the requested commit is not indexed).
type AdjustedDiagnostic struct {
	lsifstore.Diagnostic
	Dump           store.Dump
	AdjustedCommit string
	AdjustedRange  lsifstore.Range
}

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

// uploadIDs returns a slice of this query's matched upload identifiers.
func (r *queryResolver) uploadIDs() []string {
	uploadIDs := make([]string, 0, len(r.uploads))
	for i := range r.uploads {
		uploadIDs = append(uploadIDs, strconv.Itoa(r.uploads[i].ID))
	}

	return uploadIDs
}

// adjustLocations translates a list of locations into a list of equivalent locations in the requested commit.
func (r *queryResolver) adjustLocations(ctx context.Context, dump store.Dump, locations []lsifstore.Location) ([]AdjustedLocation, error) {
	var resolvedLocations []codeintelapi.ResolvedLocation
	for _, location := range locations {
		resolvedLocations = append(resolvedLocations, codeintelapi.ResolvedLocation{
			Dump:  dump,
			Path:  dump.Root + location.Path,
			Range: location.Range,
		})
	}

	adjustedLocations := make([]AdjustedLocation, 0, len(resolvedLocations))
	for i := range resolvedLocations {
		adjustedCommit, adjustedRange, err := r.adjustRange(ctx, resolvedLocations[i].Dump.RepositoryID, resolvedLocations[i].Dump.Commit, resolvedLocations[i].Path, resolvedLocations[i].Range)
		if err != nil {
			return nil, err
		}

		adjustedLocations = append(adjustedLocations, AdjustedLocation{
			Dump:           resolvedLocations[i].Dump,
			Path:           resolvedLocations[i].Path,
			AdjustedCommit: adjustedCommit,
			AdjustedRange:  adjustedRange,
		})
	}

	return adjustedLocations, nil
}

// adjustRange translates a range (relative to the indexed commit) into an equivalent range in the requested commit.
func (r *queryResolver) adjustRange(ctx context.Context, repositoryID int, commit, path string, rx lsifstore.Range) (string, lsifstore.Range, error) {
	if repositoryID != r.repositoryID {
		// No diffs exist for translation between repos
		return commit, rx, nil
	}

	if _, adjustedRange, ok, err := r.positionAdjuster.AdjustRange(ctx, commit, path, rx, true); err != nil {
		return "", lsifstore.Range{}, err
	} else if ok {
		return r.commit, adjustedRange, nil
	}

	return commit, rx, nil
}
