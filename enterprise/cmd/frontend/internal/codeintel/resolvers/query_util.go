package resolvers

import (
	"context"
	"strconv"
	"strings"

	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
)

// uploadIDs returns a slice of this query's matched upload identifiers.
func (r *queryResolver) uploadIDs() []string {
	uploadIDs := make([]string, 0, len(r.uploads))
	for i := range r.uploads {
		uploadIDs = append(uploadIDs, strconv.Itoa(r.uploads[i].ID))
	}

	return uploadIDs
}

type adjustedUpload struct {
	Upload               store.Dump
	AdjustedPath         string
	AdjustedPosition     lsifstore.Position
	AdjustedPathInBundle string
}

// TODO - document
func (r *queryResolver) adjustUploads(ctx context.Context, line, character int) ([]adjustedUpload, error) {
	position := lsifstore.Position{
		Line:      line,
		Character: character,
	}

	adjustedUploads := make([]adjustedUpload, 0, len(r.uploads))
	for i := range r.uploads {
		adjustedPath, adjustedPosition, ok, err := r.positionAdjuster.AdjustPosition(ctx, r.uploads[i].Commit, r.path, position, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		adjustedUploads = append(adjustedUploads, adjustedUpload{
			Upload:               r.uploads[i],
			AdjustedPath:         adjustedPath,
			AdjustedPosition:     adjustedPosition,
			AdjustedPathInBundle: strings.TrimPrefix(adjustedPath, r.uploads[i].Root),
		})
	}

	return adjustedUploads, nil
}

// TODO - document
func (r *queryResolver) uploadsByIDs(ctx context.Context, ids []int) ([]store.Dump, error) {
	uploads, err := r.dbStore.GetDumpByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	filtered := uploads[:0]

	for _, upload := range uploads {
		commitExists, err := r.cachedCommitChecker.Exists(ctx, upload.RepositoryID, upload.Commit)
		if err != nil {
			return nil, err
		}
		if !commitExists {
			continue
		}

		filtered = append(filtered, upload)
	}

	return filtered, nil
}

// TODO - document
func (r *queryResolver) orderedMonikers(ctx context.Context, adjustedUploads []adjustedUpload, kind string) ([]lsifstore.QualifiedMonikerData, error) {
	monikerSet := newQualifiedMonikerSet()

	for i := range adjustedUploads {
		rangeMonikers, err := r.lsifStore.MonikersByPosition(
			ctx,
			adjustedUploads[i].Upload.ID,
			strings.TrimPrefix(adjustedUploads[i].AdjustedPath, adjustedUploads[i].Upload.Root),
			adjustedUploads[i].AdjustedPosition.Line,
			adjustedUploads[i].AdjustedPosition.Character,
		)
		if err != nil {
			return nil, err
		}

		for _, monikers := range rangeMonikers {
			for _, moniker := range monikers {
				if moniker.PackageInformationID == "" || (kind != "" && moniker.Kind != kind) {
					continue
				}

				packageInformationData, _, err := r.lsifStore.PackageInformation(
					ctx,
					adjustedUploads[i].Upload.ID,
					strings.TrimPrefix(adjustedUploads[i].AdjustedPath, adjustedUploads[i].Upload.Root),
					string(moniker.PackageInformationID),
				)
				if err != nil {
					return nil, err
				}

				monikerSet.add(lsifstore.QualifiedMonikerData{
					MonikerData:            moniker,
					PackageInformationData: packageInformationData,
				})
			}
		}
	}

	return monikerSet.monikers, nil
}

type qualifiedMonikerSet struct {
	monikers       []lsifstore.QualifiedMonikerData
	monikerHashMap map[string]struct{}
}

func newQualifiedMonikerSet() *qualifiedMonikerSet {
	return &qualifiedMonikerSet{
		monikerHashMap: map[string]struct{}{},
	}
}

func (s *qualifiedMonikerSet) add(qualifiedMoniker lsifstore.QualifiedMonikerData) {
	monikerHash := strings.Join([]string{
		qualifiedMoniker.PackageInformationData.Name,
		qualifiedMoniker.PackageInformationData.Version,
		qualifiedMoniker.MonikerData.Scheme,
		qualifiedMoniker.MonikerData.Identifier,
	}, ":")

	if _, ok := s.monikerHashMap[monikerHash]; ok {
		return
	}

	s.monikerHashMap[monikerHash] = struct{}{}
	s.monikers = append(s.monikers, qualifiedMoniker)
}

// TODO - document
func (r *queryResolver) monikerLocations(ctx context.Context, uploads []dbstore.Dump, orderedMonikers []lsifstore.QualifiedMonikerData, tableName string, limit, offset int) ([]lsifstore.Location, error) {
	ids := make([]int, 0, len(uploads))
	for _, upload := range uploads {
		ids = append(ids, upload.ID)
	}

	args := make([]lsifstore.MonikerData, 0, len(orderedMonikers))
	for _, moniker := range orderedMonikers {
		args = append(args, moniker.MonikerData)
	}

	locations, _, err := r.lsifStore.BulkMonikerResults(ctx, tableName, ids, args, limit, offset)
	if err != nil {
		return nil, err
	}

	return locations, nil
}

// TODO - document
func (r *queryResolver) adjustLocations(ctx context.Context, uploadsByID map[int]dbstore.Dump, locations []lsifstore.Location) ([]AdjustedLocation, error) {
	adjustedLocations := make([]AdjustedLocation, 0, len(locations))
	for _, location := range locations {
		adjustedLocation, err := r.adjustLocation(ctx, uploadsByID[location.DumpID], location)
		if err != nil {
			return nil, err
		}

		adjustedLocations = append(adjustedLocations, adjustedLocation)
	}

	return adjustedLocations, nil
}

// TODO - document
func (r *queryResolver) adjustLocation(ctx context.Context, dump store.Dump, location lsifstore.Location) (AdjustedLocation, error) {
	adjustedCommit, adjustedRange, err := r.adjustRange(ctx, dump.RepositoryID, dump.Commit, dump.Root+location.Path, location.Range)
	if err != nil {
		return AdjustedLocation{}, err
	}

	return AdjustedLocation{
		Dump:           dump,
		Path:           dump.Root + location.Path,
		AdjustedCommit: adjustedCommit,
		AdjustedRange:  adjustedRange,
	}, nil
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
