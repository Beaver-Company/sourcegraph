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

	// TODO - log more things here

	//
	// TODO - document the following block

	adjustedUploads, err := r.adjustUploads(ctx, line, character)
	if err != nil {
		return "", lsifstore.Range{}, false, err
	}

	//
	// TODO - document the following block

	for i := range adjustedUploads {
		text, rn, exists, err := r.lsifStore.Hover(
			ctx,
			r.uploads[i].ID,
			adjustedUploads[i].AdjustedPathInBundle,
			adjustedUploads[i].AdjustedPosition.Line,
			adjustedUploads[i].AdjustedPosition.Character,
		)
		if err != nil {
			return "", lsifstore.Range{}, false, err
		}
		if !exists || text == "" {
			continue
		}

		_, adjustedRange, err := r.adjustRange(ctx, r.uploads[i].RepositoryID, r.uploads[i].Commit, r.path, rn)
		if err != nil {
			return "", lsifstore.Range{}, false, err
		}

		return text, adjustedRange, true, nil
	}

	return "", lsifstore.Range{}, false, nil
}
