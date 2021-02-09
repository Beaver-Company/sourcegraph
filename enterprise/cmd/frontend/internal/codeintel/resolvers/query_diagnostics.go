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

const slowDiagnosticsRequestThreshold = time.Second

// Diagnostics returns the diagnostics for documents with the given path prefix.
func (r *queryResolver) Diagnostics(ctx context.Context, limit int) (_ []AdjustedDiagnostic, _ int, err error) {
	ctx, endObservation := observeResolver(ctx, &err, "Diagnostics", r.operations.diagnostics, slowDiagnosticsRequestThreshold, observation.Args{
		LogFields: []log.Field{
			log.Int("repositoryID", r.repositoryID),
			log.String("commit", r.commit),
			log.String("path", r.path),
			log.String("uploadIDs", strings.Join(r.uploadIDs(), ", ")),
			log.Int("limit", limit),
		},
	})
	defer endObservation()

	// TODO - log result size
	// TODO - log more things here

	type sliceOfWork struct {
		Upload       store.Dump
		AdjustedPath string
		Diagnostics  []lsifstore.Diagnostic
	}
	var worklist []sliceOfWork

	// Step 1: Seed the worklist with the adjusted path and position for each candidate upload.
	// If an upload is attached to a commit with no equivalent path or position, that candidate
	// is skipped.

	for i := range r.uploads {
		adjustedPath, ok, err := r.positionAdjuster.AdjustPath(ctx, r.uploads[i].Commit, r.path, false)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			continue
		}

		worklist = append(worklist, sliceOfWork{
			Upload:       r.uploads[i],
			AdjustedPath: adjustedPath,
		})
	}

	// Phase 2: Perform a diagnostics query for each viable upload candidate with the adjusted
	// path and position. As a side-effect, this phase will count the total number of items that
	// will be returned in `n`, and the total number of items in the entire result set in
	// `totalCount`.

	n := 0
	totalCount := 0

	for i := range worklist {
		diagnostics, count, err := r.lsifStore.Diagnostics(
			ctx,
			worklist[i].Upload.ID,
			strings.TrimPrefix(worklist[i].AdjustedPath, worklist[i].Upload.Root),
			0,
			limit,
		)
		if err != nil {
			return nil, 0, err
		}

		totalCount += count
		n += len(diagnostics)
		worklist[i].Diagnostics = diagnostics
	}

	// Phase 3: Combine all diagnostic results and re-adjust the locations in the output ranges
	// so they target the same commit that the user has requested diagnostic results for.

	adjustedDiagnostics := make([]AdjustedDiagnostic, 0, n)
	for i := range worklist {
		for _, diagnostic := range worklist[i].Diagnostics {
			diagnostic = lsifstore.Diagnostic{
				DumpID:         diagnostic.DumpID,
				Path:           worklist[i].Upload.Root + diagnostic.Path,
				DiagnosticData: diagnostic.DiagnosticData,
			}

			adjustedCommit, adjustedRange, err := r.adjustRange(
				ctx,
				worklist[i].Upload.RepositoryID,
				worklist[i].Upload.Commit,
				diagnostic.Path,
				lsifstore.Range{
					Start: lsifstore.Position{Line: diagnostic.StartLine, Character: diagnostic.StartCharacter},
					End:   lsifstore.Position{Line: diagnostic.EndLine, Character: diagnostic.EndCharacter},
				},
			)
			if err != nil {
				return nil, 0, err
			}

			adjustedDiagnostics = append(adjustedDiagnostics, AdjustedDiagnostic{
				Diagnostic:     diagnostic,
				Dump:           worklist[i].Upload,
				AdjustedCommit: adjustedCommit,
				AdjustedRange:  adjustedRange,
			})
		}
	}

	if len(adjustedDiagnostics) > limit {
		adjustedDiagnostics = adjustedDiagnostics[:limit]
	}

	return adjustedDiagnostics, totalCount, nil
}
