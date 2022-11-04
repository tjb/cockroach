// Copyright 2022 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package bootstrap

import (
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/rowenc"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/catid"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/util"
	"github.com/cockroachdb/errors"
)

// KVWriter is used to transform SQL table records into KV pairs.
// This is useful for bypassing the internal executor and writing to tables
// directly.
//
// Care should be exercised to only use this in contexts which are known to be
// safe. Populating system tables while bootstrapping a cluster is one of these.
type KVWriter struct {
	codec            keys.SQLCodec
	tableDesc        catalog.TableDescriptor
	colIDtoRowIndex  catalog.TableColMap
	skippedFamilyIDs util.FastIntSet
}

// RecordToKeyValues transforms a table record into the corresponding key-value
// pairs.
func (w KVWriter) RecordToKeyValues(values ...tree.Datum) (ret []roachpb.KeyValue, _ error) {
	if expected, actual := w.colIDtoRowIndex.Len(), len(values); expected != actual {
		return nil, errors.AssertionFailedf(
			"expected %d datum(s), instead got %d", expected, actual,
		)
	}

	// Encode the primary index row.
	{
		idx := w.tableDesc.GetPrimaryIndex()
		indexEntries, err := rowenc.EncodePrimaryIndex(
			w.codec, w.tableDesc, idx, w.colIDtoRowIndex, values, true, /* includeEmpty */
		)
		if err != nil {
			return nil, errors.NewAssertionErrorWithWrappedErrf(
				err, "encoding for primary index %q (%d)", idx.GetName(), idx.GetID(),
			)
		}
		for _, e := range indexEntries {
			if w.skippedFamilyIDs.Contains(int(e.Family)) {
				continue
			}
			ret = append(ret, roachpb.KeyValue{Key: e.Key, Value: e.Value})
		}
	}

	// Encode the secondary index rows.
	for _, idx := range w.tableDesc.PublicNonPrimaryIndexes() {
		indexEntries, err := rowenc.EncodeSecondaryIndex(
			w.codec, w.tableDesc, idx, w.colIDtoRowIndex, values, true, /* includeEmpty */
		)
		if err != nil {
			return nil, errors.NewAssertionErrorWithWrappedErrf(
				err, "encoding for secondary index %q (%d)", idx.GetName(), idx.GetID(),
			)
		}
		for _, e := range indexEntries {
			if w.skippedFamilyIDs.Contains(int(e.Family)) {
				continue
			}
			ret = append(ret, roachpb.KeyValue{Key: e.Key, Value: e.Value})
		}
	}

	return ret, nil
}

// Insert updates a batch with the KV operations required to insert a new record
// into the table.
func (w KVWriter) Insert(b *kv.Batch, values ...tree.Datum) error {
	kvs, err := w.RecordToKeyValues(values...)
	if err != nil {
		return err
	}
	for _, kv := range kvs {
		b.CPutAllowingIfNotExists(kv.Key, &kv.Value, nil /* expValue */)
	}
	return nil
}

// Delete updates a batch with the KV operations required to delete an existing
// record from the table.
func (w KVWriter) Delete(b *kv.Batch, values ...tree.Datum) error {
	kvs, err := w.RecordToKeyValues(values...)
	if err != nil {
		return err
	}
	for _, kv := range kvs {
		b.Del(kv.Key)
	}
	return nil
}

// MakeKVWriter constructs a KVWriter instance.
func MakeKVWriter(
	codec keys.SQLCodec, tableDesc catalog.TableDescriptor, skippedColumnFamilyIDs ...catid.FamilyID,
) KVWriter {
	w := KVWriter{
		codec:     codec,
		tableDesc: tableDesc,
	}
	for i, c := range tableDesc.PublicColumns() {
		w.colIDtoRowIndex.Set(c.GetID(), i)
	}
	for _, id := range skippedColumnFamilyIDs {
		w.skippedFamilyIDs.Add(int(id))
	}
	return w
}
