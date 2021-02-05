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

const slowHoverRequestThreshold = time.Second

// Hover returns the hover text and range for the symbol at the given position. If there are
// multiple bundles associated with this resolver, the hover text and range from the first
// bundle with any results will be returned.
func (r *queryResolver) Hover(ctx context.Context, line, character int) (_ string, _ lsifstore.Range, _ bool, err error) {
	ctx, endObservation := observeResolver(ctx, &err, "Hover", r.operations.hover, slowHoverRequestThreshold, observation.Args{
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

	type sliceOfWork struct {
		Upload           store.Dump
		AdjustedPath     string
		AdjustedPosition lsifstore.Position
		Text             string
		Range            lsifstore.Range
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
			return "", lsifstore.Range{}, false, err
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

	// Phase 2: Perform a hover query for each viable upload candidate with the adjusted path
	// and position.

	for i := range worklist {
		text, r, exists, err := r.lsifStore.Hover(
			ctx,
			worklist[i].Upload.ID,
			strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
			worklist[i].AdjustedPosition.Line,
			worklist[i].AdjustedPosition.Character,
		)
		if err != nil {
			return "", lsifstore.Range{}, false, err
		}
		if !exists || text == "" {
			continue
		}

		worklist[i].Text = text
		worklist[i].Range = r
	}

	// Phase 5: Return the hover text attached to the inner-most range (with non-empty results)
	// and re-adjust the associated ranges so that it targets the same commit that the user has
	// requested hover results for.

	for i := range worklist {
		if worklist[i].Text != "" {
			_, adjustedRange, ok, err := r.positionAdjuster.AdjustRange(
				ctx,
				worklist[i].Upload.Commit,
				r.path,
				worklist[i].Range,
				true,
			)
			if err != nil || !ok {
				return "", lsifstore.Range{}, false, err
			}

			return worklist[i].Text, adjustedRange, true, nil
		}
	}

	return "", lsifstore.Range{}, false, nil
}
