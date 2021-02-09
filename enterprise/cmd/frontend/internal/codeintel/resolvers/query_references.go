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

	// TODO - log result size
	// TODO - log more things here
	// TODO - paginate

	//
	// TODO - document the following block

	adjustedUploads, err := r.adjustUploads(ctx, line, character)
	if err != nil {
		return nil, "", err
	}

	// Make map for quick lookup by identifier
	uploadsByID := make(map[int]dbstore.Dump, len(adjustedUploads))
	for i := range adjustedUploads {
		uploadsByID[adjustedUploads[i].Upload.ID] = adjustedUploads[i].Upload
	}

	//
	// TODO - document the following block

	var localLocations []lsifstore.Location

	for i := range adjustedUploads {
		locations, err := r.lsifStore.References(
			ctx,
			adjustedUploads[i].Upload.ID,
			adjustedUploads[i].AdjustedPathInBundle,
			adjustedUploads[i].AdjustedPosition.Line,
			adjustedUploads[i].AdjustedPosition.Character,
		)
		if err != nil {
			return nil, "", err
		}

		localLocations = append(localLocations, locations...)
	}

	//
	// TODO - document the following block

	orderedMonikers, err := r.orderedMonikers(ctx, adjustedUploads, "")
	if err != nil {
		return nil, "", err
	}

	uploads, err := r.getUploadsWithReferences(ctx, adjustedUploads, orderedMonikers)
	if err != nil {
		return nil, "", err
	}

	// Add new uploads to map
	for _, upload := range uploads {
		uploadsByID[upload.ID] = upload
	}

	//
	// TODO - document the following block

	locations, err := r.monikerLocations(ctx, uploads, orderedMonikers, "references", 10000000, 0)
	if err != nil {
		return nil, "", err
	}

	filtered := locations[:0]
	for _, location := range locations {
		if !isSourceLocation(adjustedUploads, location) {
			filtered = append(filtered, location)
		}
	}

	adjustedLocations, err := r.adjustLocations(ctx, uploadsByID, append(localLocations, filtered...))
	if err != nil {
		return nil, "", err
	}

	return adjustedLocations, "", nil
}

// TODO - document
func (r *queryResolver) getUploadsWithReferences(ctx context.Context, adjustedUploads []adjustedUpload, orderedMonikers []lsifstore.QualifiedMonikerData) ([]store.Dump, error) {
	uploadsByID := map[int]dbstore.Dump{}
	for i := range adjustedUploads {
		uploadsByID[adjustedUploads[i].Upload.ID] = adjustedUploads[i].Upload
	}

	packageIDs, err := r.dbStore.PackageIDs(ctx, orderedMonikers)
	if err != nil {
		return nil, err
	}

	referenceIDsAndFilters, err := r.dbStore.ReferenceIDsAndFilters(ctx, r.repositoryID, r.commit, orderedMonikers)
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

	var uploads []store.Dump
	var missingIDs []int

	for _, dumpID := range dumpIDs {
		if dump, ok := uploadsByID[dumpID]; ok {
			uploads = append(uploads, dump)
		} else {
			missingIDs = append(missingIDs, dumpID)
		}
	}

	otherUploads, err := r.uploadsByIDs(ctx, missingIDs)
	if err != nil {
		return nil, err
	}

	return append(uploads, otherUploads...), nil
}

// TODO - document
func isSourceLocation(adjustedUploads []adjustedUpload, location lsifstore.Location) bool {
	for i := range adjustedUploads {
		if location.DumpID == adjustedUploads[i].Upload.ID && location.Path == adjustedUploads[i].AdjustedPath {
			if rangeContainsPosition(location.Range, adjustedUploads[i].AdjustedPosition) {
				return true
			}
		}
	}

	return false
}

// TODO - document
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
