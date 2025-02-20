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

package pir

var rules = []func(t *Trace) error{
	checkSortSize,
}

func checkSortSize(t *Trace) error {
	f, ok := t.Final().(*Order)
	if !ok {
		return nil
	}
	if c := t.Class(); c > SizeColumnCardinality {
		return errorf(f.Columns[0].Column, "cannot perform ORDER BY with unlimited cardinality")
	}
	return nil
}

func postcheck(t *Trace) error {
	for _, r := range rules {
		if err := r(t); err != nil {
			return err
		}
	}
	return nil
}
