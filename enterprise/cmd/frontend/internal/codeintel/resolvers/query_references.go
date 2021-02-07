package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/opentracing/opentracing-go/log"

	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/bloomfilter"
	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

const slowReferencesRequestThreshold = time.Second

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

	worklist, err := r.seed(ctx, line, character)
	if err != nil {
		return nil, "", err
	}

	orderedMonikers, err := r.fooboo(ctx, worklist)
	if err != nil {
		return nil, "", err
	}

	dumpIDs, err := r.doAThing(ctx, orderedMonikers)
	if err != nil {
		return nil, "", err
	}

	temp := dumpIDs
	dumpIDs = dumpIDs[:0]

outerx:
	for _, dumpID := range temp {
		for i := range worklist {
			if dumpID == worklist[i].Upload.ID {
				continue outerx
			}
		}

		dumpIDs = append(dumpIDs, dumpID)
	}

	// TODO - redocument
	// Phase 2: Perform a references query for each viable upload candidate with the adjusted
	// path and position. This will return references linked to the given position via the LSIF
	// graph and does not include cross-index results.

	// TODO - combine both of these phases together

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

		locationSet := NewLocationSet()
		for _, l := range locations {
			_ = locationSet.Add(l)
		}

		for _, moniker := range orderedMonikers {
			// TODO - batch these
			temp, _, err := r.lsifStore.MonikerResults(ctx, worklist[i].Upload.ID, "references", moniker.Scheme, moniker.Identifier, 0, 10000000)
			if err != nil {
				return nil, "", err
			}

			for _, l := range temp {
				if locationSet.Add(l) {
					locations = append(locations, l)
				}
			}
		}

		if len(locations) > 0 {
			qualifiedLocations = append(qualifiedLocations, QualifiedLocations{
				Upload:    worklist[i].Upload,
				Locations: locations,
			})
		}
	}

	uploads, err := r.dbStore.GetDumpByIDs(ctx, dumpIDs)
	if err != nil {
		return nil, "", err
	}

	uploadMap := map[int]store.Dump{}
	for _, upload := range uploads {
		uploadMap[upload.ID] = upload
	}

	var realDumpIDs []int
	for dumpID, upload := range uploadMap {
		commitExists, err := r.cachedCommitChecker.Exists(ctx, upload.RepositoryID, upload.Commit)
		if err != nil {
			return nil, "", err
		}
		if !commitExists {
			continue
		}

		realDumpIDs = append(realDumpIDs, dumpID)
	}

	//
	// TODO - batch these for easier pagination
	//

	for _, dumpID := range realDumpIDs {
		for _, moniker := range orderedMonikers {
			// TODO - batch these
			locations, _, err := r.lsifStore.MonikerResults(ctx, dumpID, "references", moniker.Scheme, moniker.Identifier, 0, 10000000)
			if err != nil {
				return nil, "", err
			}

			qualifiedLocations = append(qualifiedLocations, QualifiedLocations{
				Upload:    uploadMap[dumpID],
				Locations: locations,
			})
		}
	}

	// Phase 6: Combine all reference results and re-adjust the locations in the output ranges
	// so they target the same commit that the user has requested diagnostic results for.

	var combinedLocations []AdjustedLocation
	for j := range qualifiedLocations {
		adjustedLocations, err := r.adjustLocations(ctx, qualifiedLocations[j].Upload, qualifiedLocations[j].Locations)
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

// TODO - test
// TODO - document
// TODO - rename
// TODO - refactor
func (r *queryResolver) doAThing(ctx context.Context, orderedMonikers []QualifiedMoniker) ([]int, error) {
	var rx []lsifstore.DumpAndFilter
	for _, moniker := range orderedMonikers {
		references, err := r.dbStore.AllTheStuff(ctx, r.repositoryID, r.commit, moniker.Scheme, moniker.Name, moniker.Version)
		if err != nil {
			return nil, err
		}

		rx = append(rx, references...)
	}

	// TODO - make a datastructure for this as well?
	// TODO - clean this up
	var dumps []int
	filters := map[int][][]byte{}

	for _, reference := range rx {
		if _, ok := filters[reference.DumpID]; !ok {
			dumps = append(dumps, reference.DumpID)
		}

		filters[reference.DumpID] = append(filters[reference.DumpID], reference.Filter)
	}

	var dumpIDs []int

dl:
	for _, dumpID := range dumps {
		for _, filter := range filters[dumpID] {
			if len(filter) == 0 {
				dumpIDs = append(dumpIDs, dumpID)
				continue dl
			}

			for _, moniker := range orderedMonikers {
				includesIdentifier, err := bloomfilter.DecodeAndTestFilter(filter, moniker.Identifier)
				if err != nil {
					return nil, err
				}
				if includesIdentifier {
					dumpIDs = append(dumpIDs, dumpID)
					continue dl
				}
			}
		}
	}

	return dumpIDs, nil
}

//
//
//

// TODO - test
// TODO - document
// TODO - rename
// TODO - refactor
func (r *queryResolver) fooboo(ctx context.Context, worklist []sliceOfWork) ([]QualifiedMoniker, error) {
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

				_ = monikerSet.Add(QualifiedMoniker{
					MonikerData:            moniker,
					PackageInformationData: packageInformationData,
				})
			}
		}
	}

	return monikerSet.Monikers(), nil
}

// TODO - move, rename
type QualifiedMoniker struct {
	lsifstore.MonikerData
	lsifstore.PackageInformationData
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

func (s *QualifiedMonikerSet) Add(qualifiedMoniker QualifiedMoniker) bool {
	monikerHash := fmt.Sprintf(
		"%s:%s:%s:%s",
		qualifiedMoniker.PackageInformationData.Name,
		qualifiedMoniker.PackageInformationData.Version,
		qualifiedMoniker.MonikerData.Scheme,
		qualifiedMoniker.MonikerData.Identifier,
	)

	if _, ok := s.monikerHashMap[monikerHash]; ok {
		return false
	}

	s.monikerHashMap[monikerHash] = struct{}{}
	s.monikers = append(s.monikers, qualifiedMoniker)
	return true
}

//
//
//

// TODO - move, rename
type QualifiedLocations struct {
	Upload    store.Dump
	Locations []lsifstore.Location
}

type LocationSet struct {
	locationHashMap map[string]struct{}
}

func NewLocationSet() *LocationSet {
	return &LocationSet{
		locationHashMap: map[string]struct{}{},
	}
}

func (s *LocationSet) Add(location lsifstore.Location) bool {
	hash := fmt.Sprintf(
		"%s:%d:%d:%d:%d",
		location.Path,
		location.Range.Start.Line,
		location.Range.Start.Character,
		location.Range.End.Line,
		location.Range.End.Character,
	)

	if _, ok := s.locationHashMap[hash]; ok {
		return false
	}

	s.locationHashMap[hash] = struct{}{}
	return true
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
func (r *queryResolver) seed(ctx context.Context, line, character int) ([]sliceOfWork, error) {
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
