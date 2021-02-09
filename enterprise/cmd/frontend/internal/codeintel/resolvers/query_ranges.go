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

const slowRangesRequestThreshold = time.Second

// Ranges returns code intelligence for the ranges that fall within the given range of lines. These
// results are partial and do not include references outside the current file, or any location that
// requires cross-linking of bundles (cross-repo or cross-root).
func (r *queryResolver) Ranges(ctx context.Context, startLine, endLine int) (_ []AdjustedCodeIntelligenceRange, err error) {
	ctx, endObservation := observeResolver(ctx, &err, "Ranges", r.operations.ranges, slowRangesRequestThreshold, observation.Args{
		LogFields: []log.Field{
			log.Int("repositoryID", r.repositoryID),
			log.String("commit", r.commit),
			log.String("path", r.path),
			log.String("uploadIDs", strings.Join(r.uploadIDs(), ", ")),
			log.Int("startLine", startLine),
			log.Int("endLine", endLine),
		},
	})
	defer endObservation()

	type sliceOfWork struct {
		Upload       store.Dump
		AdjustedPath string
		Ranges       []lsifstore.CodeIntelligenceRange
	}
	worklist := make([]sliceOfWork, 0, len(r.uploads))

	// Step 1: Seed the worklist with the adjusted path and position for each candidate upload.
	// If an upload is attached to a commit with no equivalent path or position, that candidate
	// is skipped.

	for i := range r.uploads {
		// TODO(efritz) - attempt to adjust lines (not just paths)
		adjustedPath, ok, err := r.positionAdjuster.AdjustPath(ctx, r.uploads[i].Commit, r.path, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		worklist = append(worklist, sliceOfWork{
			Upload:       r.uploads[i],
			AdjustedPath: adjustedPath,
		})
	}

	// Phase 2: Perform a ranges query for each viable upload candidate with the adjusted path and
	// position. As a side-effect, this phase will count the total number of range items that will
	// be returned in `n`.

	n := 0

	for i := range worklist {
		ranges, err := r.lsifStore.Ranges(
			ctx,
			worklist[i].Upload.ID,
			strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
			startLine,
			endLine,
		)
		if err != nil {
			return nil, err
		}

		n += len(ranges)
		worklist[i].Ranges = ranges
	}

	// Phase 3: Combine all range results and re-adjust the locations in the output ranges so they
	// target the same commit that the user has requested range results for.

	ranges := make([]AdjustedCodeIntelligenceRange, 0, n)
	for i := range worklist {
		for _, rn := range worklist[i].Ranges {
			_, adjustedRange, err := r.adjustRange(
				ctx,
				worklist[i].Upload.RepositoryID,
				worklist[i].Upload.Commit,
				worklist[i].AdjustedPath,
				rn.Range,
			)
			if err != nil {
				return nil, err
			}

			adjustedDefinitions := make([]AdjustedLocation, 0, len(rn.Definitions))
			for _, location := range rn.Definitions {
				adjustedLocation, err := r.adjustLocation(ctx, worklist[i].Upload, location)
				if err != nil {
					return nil, err
				}

				adjustedDefinitions = append(adjustedDefinitions, adjustedLocation)
			}

			adjustedReferences := make([]AdjustedLocation, 0, len(rn.References))
			for _, location := range rn.References {
				adjustedLocation, err := r.adjustLocation(ctx, worklist[i].Upload, location)
				if err != nil {
					return nil, err
				}

				adjustedReferences = append(adjustedReferences, adjustedLocation)
			}

			ranges = append(ranges, AdjustedCodeIntelligenceRange{
				Range:       adjustedRange,
				Definitions: adjustedDefinitions,
				References:  adjustedReferences,
				HoverText:   rn.HoverText,
			})
		}
	}

	return ranges, nil
}
