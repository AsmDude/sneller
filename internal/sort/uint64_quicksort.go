// Copyright (C) 2022 Sneller, Inc.
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package sort

// Code generated by generator.go; DO NOT EDIT.

import (
	"fmt"
)

type scalarSortArgumentsUint64 struct {
	keys        []uint64
	indices     []uint64
	consumer    SortedDataConsumer
	mindistance int
}

func quicksortUint64(keys []uint64, indices []uint64, pool ThreadPool, direction Direction, consumer SortedDataConsumer, rp *RuntimeParameters) error {
	if len(keys) != len(indices) {
		return fmt.Errorf("keys and indices lengths have to be equal")
	}

	args := scalarSortArgumentsUint64{
		keys:        keys,
		indices:     indices,
		consumer:    consumer,
		mindistance: rp.QuicksortSplitThreshold}

	if direction == Ascending {
		pool.Enqueue(0, len(keys)-1, scalarQuicksortAscUint64, args)
	} else {
		pool.Enqueue(0, len(keys)-1, scalarQuicksortDescUint64, args)
	}

	return nil
}
