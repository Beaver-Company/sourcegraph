package dbstore

import (
	"context"
	"database/sql"

	"github.com/keegancsmith/sqlf"
	"github.com/opentracing/opentracing-go/log"

	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/stores/lsifstore"
	"github.com/sourcegraph/sourcegraph/internal/database/basestore"
	"github.com/sourcegraph/sourcegraph/internal/database/batch"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

// scanIdentifierFilterMap scans a map of upload identifiers to identifier filters paired with
// that upload from the return value of `*Store.query`.
func scanIdentifierFilterMap(rows *sql.Rows, queryErr error) (_ map[int][][]byte, err error) {
	if queryErr != nil {
		return nil, queryErr
	}
	defer func() { err = basestore.CloseRows(rows, err) }()

	filters := map[int][][]byte{}
	for rows.Next() {
		var dumpID int
		var filter []byte
		if err := rows.Scan(&dumpID, &filter); err != nil {
			return nil, err
		}

		filters[dumpID] = append(filters[dumpID], filter)
	}

	return filters, nil
}

// TODO - document, test
func (s *Store) PackageIDs(ctx context.Context, monikers []lsifstore.QualifiedMonikerData) (_ []int, err error) {
	// TODO - observe

	if len(monikers) == 0 {
		return nil, nil
	}

	qs := make([]*sqlf.Query, 0, len(monikers))
	for _, moniker := range monikers {
		qs = append(qs, sqlf.Sprintf("(%s, %s, %s)", moniker.Scheme, moniker.Name, moniker.Version))
	}

	return basestore.ScanInts(s.Query(ctx, sqlf.Sprintf(packageIDsQuery, sqlf.Join(qs, ", "))))
}

const packageIDsQuery = `
-- source: enterprise/internal/codeintel/stores/dbstore/references.go:PackageIDs
SELECT p.dump_id FROM lsif_packages p WHERE (p.scheme, p.name, p.version) IN (%s)
`

// TODO - document, test
// TODO - batch
func (s *Store) ReferenceIDsAndFilters(ctx context.Context, repositoryID int, commit string, monikers []lsifstore.QualifiedMonikerData) (_ map[int][][]byte, err error) {
	// TODO - observe

	if len(monikers) == 0 {
		return nil, nil
	}

	qs := make([]*sqlf.Query, 0, len(monikers))
	for _, moniker := range monikers {
		qs = append(qs, sqlf.Sprintf("(%s, %s, %s)", moniker.Scheme, moniker.Name, moniker.Version))
	}

	// TODO - group in the database
	return scanIdentifierFilterMap(s.Query(ctx, sqlf.Sprintf(
		referenceIDsAndFiltersQuery,
		makeVisibleUploadsQuery(repositoryID, commit), repositoryID,
		sqlf.Join(qs, ", "),
	)))
}

const referenceIDsAndFiltersQuery = `
-- source: enterprise/internal/codeintel/stores/dbstore/references.go:ReferenceIDsAndFilters
WITH visible_uploads AS (
	(%s)
	UNION
	(SELECT uvt.upload_id FROM lsif_uploads_visible_at_tip uvt WHERE uvt.repository_id != %s)
)
SELECT r.dump_id, r.filter
FROM lsif_references r
LEFT JOIN lsif_dumps d ON d.id = r.dump_id
WHERE (r.scheme, r.name, r.version) IN (%s) AND r.dump_id IN (SELECT * FROM visible_uploads)
`

// UpdatePackages upserts package data tied to the given upload.
func (s *Store) UpdatePackages(ctx context.Context, packages []lsifstore.Package) (err error) {
	ctx, endObservation := s.operations.updatePackages.With(ctx, &err, observation.Args{LogFields: []log.Field{}})
	defer endObservation(1, observation.Args{})

	if len(packages) == 0 {
		return nil
	}

	inserter := batch.NewBatchInserter(ctx, s.Store.Handle().DB(), "lsif_packages", "dump_id", "scheme", "name", "version")
	for _, p := range packages {
		if err := inserter.Insert(ctx, p.DumpID, p.Scheme, p.Name, p.Version); err != nil {
			return err
		}
	}

	return inserter.Flush(ctx)
}

// UpdatePackageReferences inserts reference data tied to the given upload.
func (s *Store) UpdatePackageReferences(ctx context.Context, references []lsifstore.PackageReference) (err error) {
	ctx, endObservation := s.operations.updatePackageReferences.With(ctx, &err, observation.Args{LogFields: []log.Field{}})
	defer endObservation(1, observation.Args{})

	if len(references) == 0 {
		return nil
	}

	inserter := batch.NewBatchInserter(ctx, s.Store.Handle().DB(), "lsif_references", "dump_id", "scheme", "name", "version", "filter")
	for _, r := range references {
		filter := r.Filter
		// avoid not null constraint
		if r.Filter == nil {
			filter = []byte{}
		}

		if err := inserter.Insert(ctx, r.DumpID, r.Scheme, r.Name, r.Version, filter); err != nil {
			return err
		}
	}

	return inserter.Flush(ctx)
}
