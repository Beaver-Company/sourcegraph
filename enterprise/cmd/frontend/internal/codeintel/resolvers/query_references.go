package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/opentracing/opentracing-go/log"

	store "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/dbstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

const slowReferencesRequestThreshold = time.Second

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

	type QualifiedLocations struct {
		Upload    store.Dump
		Locations []lsifstore.Location
	}
	type QualifiedMoniker struct {
		lsifstore.MonikerData
		lsifstore.PackageInformationData
	}
	type sliceOfWork struct {
		Upload             store.Dump
		AdjustedPath       string
		AdjustedPosition   lsifstore.Position
		OrderedMonikers    []QualifiedMoniker
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
			return nil, "", err
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

	// Phase 2: Perform a references query for each viable upload candidate with the adjusted
	// path and position. This will return references linked to the given position via the LSIF
	// graph and does not include cross-index results.

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
			worklist[i].QualifiedLocations = append(worklist[i].QualifiedLocations, QualifiedLocations{
				Upload:    worklist[i].Upload,
				Locations: locations,
			})
		}
	}

	// Phase 3: Continue the references search by looking in other indexes. The first step here
	// is to fetch the monikers attached to the adjusted path and range for every slice of work.
	// We also resolve the package information attached to the moniker in this phase.

	for i := range worklist {
		rangeMonikers, err := r.lsifStore.MonikersByPosition(
			ctx,
			worklist[i].Upload.ID,
			strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
			worklist[i].AdjustedPosition.Line,
			worklist[i].AdjustedPosition.Character,
		)
		if err != nil {
			return nil, "", err
		}

		var orderedMonikers []QualifiedMoniker
		for _, monikers := range rangeMonikers {
			for _, moniker := range monikers {
				if moniker.PackageInformationID != "" {
					packageInformationData, _, err := r.lsifStore.PackageInformation(
						ctx,
						worklist[i].Upload.ID,
						strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
						string(moniker.PackageInformationID),
					)
					if err != nil {
						return nil, "", err
					}

					orderedMonikers = append(orderedMonikers, QualifiedMoniker{
						MonikerData:            moniker,
						PackageInformationData: packageInformationData,
					})
				}
			}
		}

		// TODO - ensure uniqueness
		worklist[i].OrderedMonikers = orderedMonikers
	}

	// Phase 4: For every slice of work that has monikers attached from the phase above, we perform
	// a moniker query on each index that defines one of those monikers. This phase returns the set
	// of references in the defining index; this handles the case where a user requested references
	// on a non-definition that is defined in another index.

	for i := range worklist {
		for _, moniker := range worklist[i].OrderedMonikers {
			definitionUpload, exists, err := r.dbStore.GetPackage(ctx, moniker.Scheme, moniker.Name, moniker.Version)
			if err != nil {
				return nil, "", err
			}
			if !exists || definitionUpload.ID == worklist[i].Upload.ID {
				continue
			}

			locations, _, err := r.lsifStore.MonikerResults(ctx, definitionUpload.ID, "references", moniker.Scheme, moniker.Identifier, 0, 10000000)
			if err != nil {
				return nil, "", err
			}

			worklist[i].QualifiedLocations = append(worklist[i].QualifiedLocations, QualifiedLocations{
				Upload:    definitionUpload,
				Locations: locations,
			})
		}
	}

	// Phase 5: For every slice of work that has monikers attached from the phase above, we perform
	// a moniker query on each index that references one of those monikers. This phase returns the
	// set of references within the same repository (but outside of the source index).

	for i := range worklist {
		for _, moniker := range worklist[i].OrderedMonikers {
			_, pager, err := r.dbStore.SameRepoPager(ctx, r.repositoryID, r.commit, moniker.Scheme, moniker.Name, moniker.Version, 10000000)
			if err != nil {
				return nil, "", err
			}
			defer func() {
				err = pager.Done(err)
			}()

			references, err := pager.PageFromOffset(ctx, 0)
			if err != nil {
				return nil, "", err
			}

			for _, reference := range references {
				upload, exists, err := r.dbStore.GetDumpByID(ctx, reference.DumpID)
				if err != nil {
					return nil, "", err
				}
				if !exists {
					continue
				}

				// TODO(efritz) - check for commit existence
				locations, _, err := r.lsifStore.MonikerResults(ctx, reference.DumpID, "references", reference.Scheme, moniker.Identifier, 0, 10000000)
				if err != nil {
					return nil, "", err
				}

				worklist[i].QualifiedLocations = append(worklist[i].QualifiedLocations, QualifiedLocations{
					Upload:    upload,
					Locations: locations,
				})
			}
		}
	}

	// Phase 6: For every slice of work that has monikers attached from the phase above, we perform
	// a moniker query on each index that references one of those monikers. This phase returns the
	// set of references outside of the source repository.

	for i := range worklist {
		for _, moniker := range worklist[i].OrderedMonikers {
			_, pager, err := r.dbStore.PackageReferencePager(ctx, moniker.Scheme, moniker.Name, moniker.Version, r.repositoryID, 10000000)
			if err != nil {
				return nil, "", err
			}
			defer func() {
				err = pager.Done(err)
			}()

			references, err := pager.PageFromOffset(ctx, 0)
			if err != nil {
				return nil, "", err
			}

			for _, reference := range references {
				upload, exists, err := r.dbStore.GetDumpByID(ctx, reference.DumpID)
				if err != nil {
					return nil, "", err
				}
				if !exists {
					continue
				}

				// TODO(efritz) - check for commit existence
				locations, _, err := r.lsifStore.MonikerResults(ctx, reference.DumpID, "references", reference.Scheme, moniker.Identifier, 0, 10000000)
				if err != nil {
					return nil, "", err
				}

				// TODO(efritz) - ensure deduplicated
				worklist[i].QualifiedLocations = append(worklist[i].QualifiedLocations, QualifiedLocations{
					Upload:    upload,
					Locations: locations,
				})
			}
		}
	}

	// Phase 7: Combine all reference results and re-adjust the locations in the output ranges
	// so they target the same commit that the user has requested diagnostic results for.

	// TODO - cleanup
	q := map[string]struct{}{}

	var allAdjustedLocations []AdjustedLocation
	for i := range worklist {
		for j := range worklist[i].QualifiedLocations {
			// TODO _ cleanup
			var lx []lsifstore.Location
			for _, l := range worklist[i].QualifiedLocations[j].Locations {
				h := hashLocation(worklist[i].Upload.ID, l)
				if _, ok := q[h]; ok {
					continue
				}

				q[h] = struct{}{}
				lx = append(lx, l)
			}

			adjustedLocations, err := r.adjustLocations(
				ctx,
				worklist[i].QualifiedLocations[j].Upload,
				lx,
			)
			if err != nil {
				return nil, "", err
			}

			allAdjustedLocations = append(allAdjustedLocations, adjustedLocations...)
		}
	}

	return allAdjustedLocations, "", nil
}

//
// TODO

func hashLocation(uploadID int, location lsifstore.Location) string {
	return fmt.Sprintf(
		"%d:%s:%d:%d:%d:%d",
		uploadID,
		location.Path,
		location.Range.Start.Line,
		location.Range.Start.Character,
		location.Range.End.Line,
		location.Range.End.Character,
	)
}
