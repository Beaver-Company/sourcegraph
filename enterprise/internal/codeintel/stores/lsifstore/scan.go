package lsifstore

import (
	"database/sql"

	"github.com/sourcegraph/sourcegraph/internal/database/basestore"
)

type qualifiedDocumentData struct {
	UploadID int
	KeyedDocumentData
}

// scanDocumentData reads qualified document data from the given row object.
func (s *Store) scanDocumentData(rows *sql.Rows, queryErr error) (_ []qualifiedDocumentData, err error) {
	if queryErr != nil {
		return nil, queryErr
	}
	defer func() { err = basestore.CloseRows(rows, err) }()

	var values []qualifiedDocumentData
	for rows.Next() {
		record, err := s.scanSingleDocumentDataObject(rows)
		if err != nil {
			return nil, err
		}

		values = append(values, record)
	}

	return values, nil
}

// scanFirstDocumentData reads qualified document data values from the given row
// object and returns the first one. If no rows match the query, a false-valued
// flag is returned.
func (s *Store) scanFirstDocumentData(rows *sql.Rows, queryErr error) (_ qualifiedDocumentData, _ bool, err error) {
	if queryErr != nil {
		return qualifiedDocumentData{}, false, queryErr
	}
	defer func() { err = basestore.CloseRows(rows, err) }()

	if rows.Next() {
		record, err := s.scanSingleDocumentDataObject(rows)
		if err != nil {
			return qualifiedDocumentData{}, false, err
		}

		return record, true, nil
	}

	return qualifiedDocumentData{}, false, nil
}

// scanSingleDocumentDataObject populates a qualified document data value from the
// given cursor.
func (s *Store) scanSingleDocumentDataObject(rows *sql.Rows) (qualifiedDocumentData, error) {
	var rawData []byte
	var record qualifiedDocumentData
	if err := rows.Scan(&record.UploadID, &record.Path, &rawData); err != nil {
		return qualifiedDocumentData{}, err
	}

	data, err := s.serializer.UnmarshalDocumentData(rawData)
	if err != nil {
		return qualifiedDocumentData{}, err
	}
	record.Document = data

	return record, nil
}

type qualifiedResultChunkData struct {
	UploadID int
	IndexedResultChunkData
}

// scanQualifiedResultChunkData reads qualified result chunk data from the given
// row object.
func (s *Store) scanQualifiedResultChunkData(rows *sql.Rows, queryErr error) (_ []qualifiedResultChunkData, err error) {
	if queryErr != nil {
		return nil, queryErr
	}
	defer func() { err = basestore.CloseRows(rows, err) }()

	var values []qualifiedResultChunkData
	for rows.Next() {
		record, err := s.scanSingleResultChunkDataObject(rows)
		if err != nil {
			return nil, err
		}

		values = append(values, record)
	}

	return values, nil
}

// scanSingleResultChunkDataObject populates a qualified result chunk data value from
// the given cursor.
func (s *Store) scanSingleResultChunkDataObject(rows *sql.Rows) (qualifiedResultChunkData, error) {
	var rawData []byte
	var record qualifiedResultChunkData
	if err := rows.Scan(&record.UploadID, &record.Index, &rawData); err != nil {
		return qualifiedResultChunkData{}, err
	}

	data, err := s.serializer.UnmarshalResultChunkData(rawData)
	if err != nil {
		return qualifiedResultChunkData{}, err
	}
	record.ResultChunk = data

	return record, nil
}

type qualifiedMonikerLocations struct {
	DumpID int
	MonikerLocations
}

// TODO - redocument
// scanQualifiedResultChunkData reads moniker locations values from the given row object.
func (s *Store) scanQualifiedLocations(rows *sql.Rows, queryErr error) (_ []qualifiedMonikerLocations, err error) {
	if queryErr != nil {
		return nil, queryErr
	}
	defer func() { err = basestore.CloseRows(rows, err) }()

	var values []qualifiedMonikerLocations
	for rows.Next() {
		record, err := s.scanSingleQualifiedMonikerLocationsObject(rows)
		if err != nil {
			return nil, err
		}

		values = append(values, record)
	}

	return values, nil
}

// TOOO - fix all of these docstrings
// scanSingleMonikerLocationsObject populates a moniker locations value from the
// given cursor.
func (s *Store) scanSingleQualifiedMonikerLocationsObject(rows *sql.Rows) (qualifiedMonikerLocations, error) {
	var rawData []byte
	var record qualifiedMonikerLocations
	if err := rows.Scan(&record.DumpID, &record.Scheme, &record.Identifier, &rawData); err != nil {
		return qualifiedMonikerLocations{}, err
	}

	data, err := s.serializer.UnmarshalLocations(rawData)
	if err != nil {
		return qualifiedMonikerLocations{}, err
	}
	record.Locations = data

	return record, nil
}

// scanFirstLocations reads a moniker locations value from the given row object and
// returns the first one. If no rows match the query, a false-valued flag is returned.
func (s *Store) scanFirstLocations(rows *sql.Rows, queryErr error) (_ MonikerLocations, _ bool, err error) {
	if queryErr != nil {
		return MonikerLocations{}, false, queryErr
	}
	defer func() { err = basestore.CloseRows(rows, err) }()

	if rows.Next() {
		record, err := s.scanSingleMonikerLocationsObject(rows)
		if err != nil {
			return MonikerLocations{}, false, err
		}

		return record, true, nil
	}

	return MonikerLocations{}, false, nil
}

// scanSingleMonikerLocationsObject populates a moniker locations value from the
// given cursor.
func (s *Store) scanSingleMonikerLocationsObject(rows *sql.Rows) (MonikerLocations, error) {
	var rawData []byte
	var record MonikerLocations
	if err := rows.Scan(&record.Scheme, &record.Identifier, &rawData); err != nil {
		return MonikerLocations{}, err
	}

	data, err := s.serializer.UnmarshalLocations(rawData)
	if err != nil {
		return MonikerLocations{}, err
	}
	record.Locations = data

	return record, nil
}
