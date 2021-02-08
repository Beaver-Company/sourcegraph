package resolvers

import (
	"context"
	"strings"
	"time"

	"github.com/opentracing/opentracing-go/log"

	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

const slowHoverRequestThreshold = time.Second

// Hover returns the hover text and range for the symbol at the given position.
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

		text, rn, exists, err := r.lsifStore.Hover(
			ctx,
			r.uploads[i].ID,
			strings.TrimPrefix(adjustedPath, r.uploads[i].Root),
			adjustedPosition.Line,
			adjustedPosition.Character,
		)
		if err != nil {
			return "", lsifstore.Range{}, false, err
		}
		if !exists || text == "" {
			continue
		}

		_, adjustedRange, ok, err := r.positionAdjuster.AdjustRange(ctx, r.uploads[i].Commit, r.path, rn, true)
		if err != nil || !ok {
			return "", lsifstore.Range{}, false, err
		}

		return text, adjustedRange, true, nil
	}

	return "", lsifstore.Range{}, false, nil
}
