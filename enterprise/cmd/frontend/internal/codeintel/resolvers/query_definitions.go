package resolvers

import (
	"context"
	"strings"
	"time"

	"github.com/opentracing/opentracing-go/log"

	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

const slowDefinitionsRequestThreshold = time.Second

// TODO - document
const defintionMonikersLimit = 100

// Definitions returns the list of source locations that define the symbol at the given position.
func (r *queryResolver) Definitions(ctx context.Context, line, character int) (_ []AdjustedLocation, err error) {
	ctx, endObservation := observeResolver(ctx, &err, "Definitions", r.operations.definitions, slowDefinitionsRequestThreshold, observation.Args{
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

	//
	// TODO - document the following block

	adjustedUploads, err := r.adjustUploads(ctx, line, character)
	if err != nil {
		return nil, err
	}

	// Make map for quick lookup by identifier
	uploadsByID := make(map[int]dbstore.Dump, len(adjustedUploads))
	for i := range adjustedUploads {
		uploadsByID[adjustedUploads[i].Upload.ID] = adjustedUploads[i].Upload
	}

	//
	// TODO - document the following block

	for i := range adjustedUploads {
		locations, err := r.lsifStore.Definitions(
			ctx,
			adjustedUploads[i].Upload.ID,
			adjustedUploads[i].AdjustedPathInBundle,
			adjustedUploads[i].AdjustedPosition.Line,
			adjustedUploads[i].AdjustedPosition.Character,
		)
		if err != nil {
			return nil, err
		}
		if len(locations) > 0 {
			return r.adjustLocations(ctx, uploadsByID, locations)
		}
	}

	//
	// TODO - document the following block

	orderedMonikers, err := r.orderedMonikers(ctx, adjustedUploads, "import")
	if err != nil {
		return nil, err
	}

	uploads, err := r.getUploadsWithDefinitions(ctx, adjustedUploads, orderedMonikers)
	if err != nil {
		return nil, err
	}

	// Add new uploads to map
	for _, upload := range uploads {
		uploadsByID[upload.ID] = upload
	}

	//
	// TODO - document the following block

	locations, err := r.monikerLocations(ctx, uploads, orderedMonikers, "definitions", defintionMonikersLimit, 0)
	if err != nil {
		return nil, err
	}

	adjustedLocations, err := r.adjustLocations(ctx, uploadsByID, locations)
	if err != nil {
		return nil, err
	}

	return adjustedLocations, nil
}

// TODO - document
func (r *queryResolver) getUploadsWithDefinitions(ctx context.Context, adjustedUploads []adjustedUpload, orderedMonikers []lsifstore.QualifiedMonikerData) ([]store.Dump, error) {
	packageIDs, err := r.dbStore.PackageIDs(ctx, orderedMonikers)
	if err != nil {
		return nil, err
	}

	uploads, err := r.uploadsByIDs(ctx, packageIDs)
	if err != nil {
		return nil, err
	}

	return uploads, nil
}
