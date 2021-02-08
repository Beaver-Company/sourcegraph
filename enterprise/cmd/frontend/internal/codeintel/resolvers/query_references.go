package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/opentracing/opentracing-go/log"

	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/bloomfilter"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

const slowReferencesRequestThreshold = time.Second

type QualifiedLocations struct {
	Upload    store.Dump
	Locations []lsifstore.Location
}

func rangeIncludesPosition(r lsifstore.Range, pos lsifstore.Position) bool {
	if pos.Line < r.Start.Line {
		return false
	}

	if pos.Line > r.End.Line {
		return false
	}

	if pos.Line == r.Start.Line && pos.Character < r.Start.Character {
		return false
	}

	if pos.Line == r.End.Line && pos.Character > r.End.Character {
		return false
	}

	return true
}

//
// TODO - paginate
//

// References returns the list of source locations that reference the symbol at the given position.
// This may include references from other dumps and repositories. If there are multiple bundles
// associated with this resolver, results from all bundles will be concatenated and returned.
func (r *queryResolver) References(ctx context.Context, line, character, limit int, rawCursor string) (_ []AdjustedLocation, _ string, err error) {
	ctx, endObservation := observeResolver(ctx, &err, "References", r.operations.references, slowReferencesRequestThreshold, observation.Args{
		LogFields: []log.Field{
			log.Int("repositoryID", r.repositoryID),
			log.String("commit", r.commit),
			log.String("path", r.path),
			log.String("uploadIDs", strings.Join(r.uploadIDs(), ", ")),
			log.Int("line", line),
			log.Int("character", character),
		},
	})
	defer endObservation()

	worklist, err := r.adjustPathsForReferences(ctx, line, character)
	if err != nil {
		return nil, "", err
	}

	orderedMonikers, err := r.getOrderedMonikers(ctx, worklist)
	if err != nil {
		return nil, "", err
	}

	uploadMap, err := r.getReferencingUploads(ctx, worklist, orderedMonikers)
	if err != nil {
		return nil, "", err
	}

	//
	// TODO

	var qualifiedLocations []QualifiedLocations
	for i := range worklist {
		locations, err := r.lsifStore.References(
			ctx,
			worklist[i].Upload.ID,
			strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
			worklist[i].AdjustedPosition.Line,
			worklist[i].AdjustedPosition.Character,
		)
		if err != nil {
			return nil, "", err
		}

		if len(locations) > 0 {
			qualifiedLocations = append(qualifiedLocations, QualifiedLocations{
				Upload:    worklist[i].Upload,
				Locations: locations,
			})
		}
	}

	//
	// TODO

	var args []lsifstore.BulkMonikerArgs
	for dumpID := range uploadMap {
		for _, moniker := range orderedMonikers {
			args = append(args, lsifstore.BulkMonikerArgs{
				BundleID:   dumpID,
				Scheme:     moniker.Scheme,
				Identifier: moniker.Identifier,
			})
		}
	}
	locations, _, err := r.lsifStore.BulkMonikerResults(ctx, "references", args, 0, 10000000)
	if err != nil {
		return nil, "", err
	}

	//
	// TODO

outer:
	for _, location := range locations {
		for i := range worklist {
			if location.DumpID == worklist[i].Upload.ID && location.Path == worklist[i].AdjustedPath && rangeIncludesPosition(location.Range, worklist[i].AdjustedPosition) {
				continue outer
			}
		}

		if n := len(qualifiedLocations); n == 0 || qualifiedLocations[n-1].Upload.ID != location.DumpID {
			qualifiedLocations = append(qualifiedLocations, QualifiedLocations{
				Upload: uploadMap[location.DumpID],
			})
		}

		n := len(qualifiedLocations) - 1
		qualifiedLocations[n].Locations = append(qualifiedLocations[n].Locations, location)
	}

	//
	// TODO

	var combinedLocations []AdjustedLocation // TODO - determine size (or use limit?)
	for i := range qualifiedLocations {
		adjustedLocations, err := r.adjustLocations(ctx, qualifiedLocations[i].Upload, qualifiedLocations[i].Locations)
		if err != nil {
			return nil, "", err
		}

		combinedLocations = append(combinedLocations, adjustedLocations...)
	}

	return combinedLocations, "", nil
}

//
//
//

// TODO - standardize this
type sliceOfWork struct {
	Upload           store.Dump
	AdjustedPath     string
	AdjustedPosition lsifstore.Position
}

// TODO - test
// TODO - document
// TODO - rename
// TODO - refactor
func (r *queryResolver) adjustPathsForReferences(ctx context.Context, line, character int) ([]sliceOfWork, error) {
	position := lsifstore.Position{
		Line:      line,
		Character: character,
	}

	worklist := make([]sliceOfWork, 0, len(r.uploads))
	for i := range r.uploads {
		adjustedPath, adjustedPosition, ok, err := r.positionAdjuster.AdjustPosition(ctx, r.uploads[i].Commit, r.path, position, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		worklist = append(worklist, sliceOfWork{
			Upload:           r.uploads[i],
			AdjustedPath:     adjustedPath,
			AdjustedPosition: adjustedPosition,
		})
	}

	return worklist, nil
}

//
//
//

// TODO - move, rename
type QualifiedMoniker struct {
	lsifstore.MonikerData
	lsifstore.PackageInformationData
}

// TODO - test
// TODO - document
// TODO - rename
// TODO - refactor
func (r *queryResolver) getOrderedMonikers(ctx context.Context, worklist []sliceOfWork) ([]QualifiedMoniker, error) {
	// TODO - redocument
	// Phase 3: Continue the references search by looking in other indexes. The first step here
	// is to fetch the monikers attached to the adjusted path and range for every slice of work.
	// We also resolve the package information attached to the moniker in this phase. This phase
	// populates the orderedMonikers.

	monikerSet := NewQualifiedMonikerSet()

	for i := range worklist {
		rangeMonikers, err := r.lsifStore.MonikersByPosition(
			ctx,
			worklist[i].Upload.ID,
			strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
			worklist[i].AdjustedPosition.Line,
			worklist[i].AdjustedPosition.Character,
		)
		if err != nil {
			return nil, err
		}

		for _, monikers := range rangeMonikers {
			for _, moniker := range monikers {
				if moniker.PackageInformationID == "" {
					continue
				}

				packageInformationData, _, err := r.lsifStore.PackageInformation(
					ctx,
					worklist[i].Upload.ID,
					strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
					string(moniker.PackageInformationID),
				)
				if err != nil {
					return nil, err
				}

				monikerSet.Add(QualifiedMoniker{
					MonikerData:            moniker,
					PackageInformationData: packageInformationData,
				})
			}
		}
	}

	return monikerSet.Monikers(), nil
}

type QualifiedMonikerSet struct {
	monikers       []QualifiedMoniker
	monikerHashMap map[string]struct{}
}

func NewQualifiedMonikerSet() *QualifiedMonikerSet {
	return &QualifiedMonikerSet{
		monikerHashMap: map[string]struct{}{},
	}
}

func (s *QualifiedMonikerSet) Monikers() []QualifiedMoniker {
	return s.monikers
}

func (s *QualifiedMonikerSet) Add(qualifiedMoniker QualifiedMoniker) {
	monikerHash := fmt.Sprintf(
		"%s:%s:%s:%s",
		qualifiedMoniker.PackageInformationData.Name,
		qualifiedMoniker.PackageInformationData.Version,
		qualifiedMoniker.MonikerData.Scheme,
		qualifiedMoniker.MonikerData.Identifier,
	)

	if _, ok := s.monikerHashMap[monikerHash]; !ok {
		s.monikerHashMap[monikerHash] = struct{}{}
		s.monikers = append(s.monikers, qualifiedMoniker)
	}
}

//
//
//

func (r *queryResolver) getReferencingUploads(ctx context.Context, worklist []sliceOfWork, orderedMonikers []QualifiedMoniker) (map[int]store.Dump, error) {
	uids := map[int]dbstore.Dump{}
	for i := range worklist {
		uids[worklist[i].Upload.ID] = worklist[i].Upload
	}

	var dfs2 []lsifstore.DumpAndFilter
	for _, moniker := range orderedMonikers {
		// TODO - batch these (will reduce duplicates)
		references2, err := r.dbStore.AllTheStuff(ctx, r.repositoryID, r.commit, moniker.Scheme, moniker.Name, moniker.Version)
		if err != nil {
			return nil, err
		}

		dfs2 = append(dfs2, references2...)
	}

	filters := map[int][][]byte{}
	for _, reference := range dfs2 {
		filters[reference.DumpID] = append(filters[reference.DumpID], reference.Filter)
	}

	var dfs1 []lsifstore.DumpAndFilter
	for _, moniker := range orderedMonikers {
		// TODO - batch these (will reduce duplicates)
		references1, err := r.dbStore.AllTheStuffX(ctx, r.repositoryID, r.commit, moniker.Scheme, moniker.Name, moniker.Version)
		if err != nil {
			return nil, err
		}
		dfs1 = append(dfs1, references1...)
	}

	for _, reference := range dfs1 {
		if _, ok := uids[reference.DumpID]; !ok {
			filters[reference.DumpID] = [][]byte{nil}
		}
	}

	var dumpIDs []int
	for dumpID, filterx := range filters {
		matchesSome := false

		for _, filter := range filterx {
			if len(filter) == 0 {
				matchesSome = true
			} else {
				// TODO - batch test
				for _, moniker := range orderedMonikers {
					includesIdentifier, err := bloomfilter.DecodeAndTestFilter(filter, moniker.Identifier)
					if err != nil {
						return nil, err
					}
					if includesIdentifier {
						matchesSome = true
					}
				}
			}
		}

		if matchesSome {
			dumpIDs = append(dumpIDs, dumpID)
		}
	}

	uploadMap := map[int]store.Dump{}
	var idsToFetch []int

	for _, dumpID := range dumpIDs {
		if dump, ok := uids[dumpID]; ok {
			uploadMap[dumpID] = dump
		} else {
			idsToFetch = append(idsToFetch, dumpID)
		}
	}

	uploads, err := r.dbStore.GetDumpByIDs(ctx, idsToFetch)
	if err != nil {
		return nil, err
	}

	for _, upload := range uploads {
		commitExists, err := r.cachedCommitChecker.Exists(ctx, upload.RepositoryID, upload.Commit)
		if err != nil {
			return nil, err
		}
		if !commitExists {
			continue
		}

		uploadMap[upload.ID] = upload
	}

	return uploadMap, nil
}
