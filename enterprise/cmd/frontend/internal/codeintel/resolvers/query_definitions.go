package resolvers

import (
	"context"
	"strings"
	"time"

	"github.com/opentracing/opentracing-go/log"

	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

const slowDefinitionsRequestThreshold = time.Second

// Definitions returns the list of source locations that define the symbol at the given position.
// This may include remote definitions if the remote repository is also indexed. If there are multiple
// bundles associated with this resolver, the definitions from the first bundle with any results will
// be returned.
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

	type QualifiedLocations struct {
		Upload    store.Dump
		Locations []lsifstore.Location
	}
	type sliceOfWork struct {
		Upload             store.Dump
		AdjustedPath       string
		AdjustedPosition   lsifstore.Position
		OrderedMonikers    []lsifstore.MonikerData
		QualifiedLocations []QualifiedLocations
	}
	var worklist []sliceOfWork

	// Step 1: Seed the worklist with the adjusted path and position for each candidate upload.
	// If an upload is attached to a commit with no equivalent path or position, that candidate
	// is skipped.

	position := lsifstore.Position{
		Line:      line,
		Character: character,
	}

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

	// Phase 2: Perform a definitions query for each viable upload candidate with the adjusted
	// path and position. This will return definitions linked to the given position via the LSIF
	// graph and does not include cross-index results.

	for i := range worklist {
		locations, err := r.lsifStore.Definitions(
			ctx,
			worklist[i].Upload.ID,
			strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
			worklist[i].AdjustedPosition.Line,
			worklist[i].AdjustedPosition.Character,
		)
		if err != nil {
			return nil, err
		}

		if len(locations) > 0 {
			worklist[i].QualifiedLocations = append(worklist[i].QualifiedLocations, QualifiedLocations{
				Upload:    worklist[i].Upload,
				Locations: locations,
			})
		}
	}

	// If we have a definition result on the first (inner-most) range, return that and skip all
	// but the last phase. This query happens to result in a local definition. Otherwise, we'll
	// fall through to cross-index definition resolution and return the results attached to the
	// inner-most range (with non-empty results).

	if len(worklist[0].QualifiedLocations) == 0 {
		// Phase 3: For every slice of work that has an empty location set, we continue the search by
		// looking in other indexes. The first step here is to fetch the monikers attached to each of
		// the ranges that returned no definition results above.

		for i := range worklist {
			if len(worklist[i].QualifiedLocations) > 0 {
				continue
			}

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

			// TODO - deduplicate here as well
			var orderedMonikers []lsifstore.MonikerData
			for _, monikers := range rangeMonikers {
				for _, moniker := range monikers {
					if moniker.Kind == "import" && moniker.PackageInformationID != "" {
						orderedMonikers = append(orderedMonikers, moniker)
					}
				}
			}

			worklist[i].OrderedMonikers = orderedMonikers
		}

		// Phase 4: For every slice of work that has monikers attached from the phase above, we perform
		// a moniker query on each index that defines one of those monikers.

		for i := range worklist {
			for _, moniker := range worklist[i].OrderedMonikers {
				// TODO - group these queries
				pid, _, err := r.lsifStore.PackageInformation(
					ctx,
					worklist[i].Upload.ID,
					strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
					string(moniker.PackageInformationID),
				)
				if err != nil {
					return nil, err
				}

				definitionUpload, exists, err := r.dbStore.GetPackage(ctx, moniker.Scheme, pid.Name, pid.Version)
				if err != nil {
					return nil, err
				}
				if !exists {
					continue
				}

				// TODO - hoist and document
				const defintionMonikersLimit = 100

				// TODO - find a way to batch these
				// TODO - share with references if possible
				locations, _, err := r.lsifStore.MonikerResults(ctx, definitionUpload.ID, "definitions", moniker.Scheme, moniker.Identifier, 0, defintionMonikersLimit)
				if err != nil {
					return nil, err
				}

				if len(locations) > 0 {
					worklist[i].QualifiedLocations = append(worklist[i].QualifiedLocations, QualifiedLocations{
						Upload:    definitionUpload,
						Locations: locations,
					})

					break
				}
			}
		}
	}

	// Phase 5: Return the results attached to the inner-most range (with non-empty results) and
	// re-adjust the output ranges so they  target the same commit that the user has requested
	// definition results for.

	for i := range worklist {
		if len(worklist[i].QualifiedLocations) == 0 {
			continue
		}

		n := 0
		for j := range worklist[i].QualifiedLocations {
			n += len(worklist[i].QualifiedLocations[j].Locations)
		}

		adjustedLocations := make([]AdjustedLocation, i, n)
		for j := range worklist[i].QualifiedLocations {
			locations, err := r.adjustLocations(
				ctx,
				worklist[i].QualifiedLocations[j].Upload,
				worklist[i].QualifiedLocations[j].Locations,
			)
			if err != nil {
				return nil, err
			}

			adjustedLocations = append(adjustedLocations, locations...)
		}

		// Return the first non-empty results attached to inner-most range
		return adjustedLocations, nil
	}

	return nil, nil
}
