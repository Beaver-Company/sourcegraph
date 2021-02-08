package resolvers

import (
	"context"
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

// References returns the list of source locations that reference the symbol at the given position.
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

	//
	// TODO - paginate
	//

	adjustedUploads, err := r.adjustUploads(ctx, line, character)
	if err != nil {
		return nil, "", err
	}

	orderedMonikers, err := r.getOrderedMonikers(ctx, adjustedUploads)
	if err != nil {
		return nil, "", err
	}

	//
	// TODO

	var combinedLocations []AdjustedLocation

	for i := range adjustedUploads {
		locations, err := r.lsifStore.References(
			ctx,
			adjustedUploads[i].Upload.ID,
			strings.TrimPrefix(adjustedUploads[i].AdjustedPath, adjustedUploads[i].Upload.Root),
			adjustedUploads[i].AdjustedPosition.Line,
			adjustedUploads[i].AdjustedPosition.Character,
		)
		if err != nil {
			return nil, "", err
		}

		for _, location := range locations {
			adjustedLocation, err := r.adjustLocation(ctx, adjustedUploads[i].Upload, location)
			if err != nil {
				return nil, "", err
			}

			combinedLocations = append(combinedLocations, adjustedLocation)
		}
	}

	//
	// TODO

	uploadMap, err := r.getUploadsWithReferences(ctx, adjustedUploads, orderedMonikers)
	if err != nil {
		return nil, "", err
	}

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

	for i := range adjustedUploads {
		uploadMap[adjustedUploads[i].Upload.ID] = adjustedUploads[i].Upload
	}

	for _, location := range locations {
		if isSourceLocation(adjustedUploads, location) {
			continue
		}

		adjustedLocation, err := r.adjustLocation(ctx, uploadMap[location.DumpID], location)
		if err != nil {
			return nil, "", err
		}

		combinedLocations = append(combinedLocations, adjustedLocation)
	}

	return combinedLocations, "", nil
}

//
//

type qualifiedMoniker struct {
	lsifstore.MonikerData
	lsifstore.PackageInformationData
}

func (r *queryResolver) getOrderedMonikers(ctx context.Context, adjustedUploads []adjustedUpload) ([]qualifiedMoniker, error) {
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
				if moniker.PackageInformationID == "" {
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

				monikerSet.add(qualifiedMoniker{
					MonikerData:            moniker,
					PackageInformationData: packageInformationData,
				})
			}
		}
	}

	return monikerSet.monikers, nil
}

type qualifiedMonikerSet struct {
	monikers       []qualifiedMoniker
	monikerHashMap map[string]struct{}
}

func newQualifiedMonikerSet() *qualifiedMonikerSet {
	return &qualifiedMonikerSet{
		monikerHashMap: map[string]struct{}{},
	}
}

func (s *qualifiedMonikerSet) add(qualifiedMoniker qualifiedMoniker) {
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

//
//

func (r *queryResolver) getUploadsWithReferences(ctx context.Context, adjustedUpload []adjustedUpload, orderedMonikers []qualifiedMoniker) (map[int]store.Dump, error) {
	uploadsByID := map[int]dbstore.Dump{}
	for i := range adjustedUpload {
		uploadsByID[adjustedUpload[i].Upload.ID] = adjustedUpload[i].Upload
	}

	var monikers []dbstore.TemporaryMonikerStruct
	for _, moniker := range orderedMonikers {
		monikers = append(monikers, dbstore.TemporaryMonikerStruct{
			Scheme:  moniker.Scheme,
			Name:    moniker.Name,
			Version: moniker.Version,
		})
	}

	packageIDs, err := r.dbStore.PackageIDs(ctx, monikers)
	if err != nil {
		return nil, err
	}

	referenceIDsAndFilters, err := r.dbStore.ReferenceIDsAndFilters(ctx, r.repositoryID, r.commit, monikers)
	if err != nil {
		return nil, err
	}

	filtersByUploadID := map[int][][]byte{}
	for dumpID, filters := range referenceIDsAndFilters {
		filtersByUploadID[dumpID] = append(filtersByUploadID[dumpID], filters...)
	}

	var dumpIDs []int
	for _, dumpID := range packageIDs {
		if _, ok := uploadsByID[dumpID]; !ok {
			dumpIDs = append(dumpIDs, dumpID)
			delete(filtersByUploadID, dumpID)
		}
	}

outer:
	for dumpID, filters := range filtersByUploadID {
		for _, filter := range filters {
			includesIdentifier, err := bloomfilter.Decode(filter)
			if err != nil {
				return nil, err
			}

			for _, moniker := range orderedMonikers {
				if includesIdentifier(moniker.Identifier) {
					dumpIDs = append(dumpIDs, dumpID)
					continue outer
				}
			}
		}
	}

	uploadMap := map[int]store.Dump{}
	var idsToFetch []int

	for _, dumpID := range dumpIDs {
		if dump, ok := uploadsByID[dumpID]; ok {
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

func isSourceLocation(worklist []adjustedUpload, location lsifstore.Location) bool {
	for i := range worklist {
		if location.DumpID == worklist[i].Upload.ID && location.Path == worklist[i].AdjustedPath {
			if rangeContainsPosition(location.Range, worklist[i].AdjustedPosition) {
				return true
			}
		}
	}

	return false
}

func rangeContainsPosition(r lsifstore.Range, pos lsifstore.Position) bool {
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
