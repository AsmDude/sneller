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

package blockfmt

import (
	"golang.org/x/exp/slices"

	"github.com/SnellerInc/sneller/date"
	"github.com/SnellerInc/sneller/ion"
)

// Range describes the (closed) interval
// that the value of a particular
// path expression could occupy
type Range interface {
	Path() []string
	Min() ion.Datum
	Max() ion.Datum
}

func NewRange(path []string, min, max ion.Datum) Range {
	if min == nil || max == nil {
		panic("blockfmt.NewRange: min/max must not be nil")
	}
	if min, ok := min.(ion.Timestamp); ok {
		if max, ok := max.(ion.Timestamp); ok {
			return &TimeRange{
				path: path,
				min:  date.Time(min),
				max:  date.Time(max),
			}
		}
	}
	return &datumRange{
		path: path,
		min:  min,
		max:  max,
	}
}

type datumRange struct {
	path     []string
	min, max ion.Datum
}

func (r *datumRange) Path() []string { return r.path }
func (r *datumRange) Min() ion.Datum { return r.min }
func (r *datumRange) Max() ion.Datum { return r.max }

type TimeRange struct {
	path []string
	min  date.Time
	max  date.Time
}

func (r *TimeRange) Path() []string     { return r.path }
func (r *TimeRange) Min() ion.Datum     { return ion.Timestamp(r.min) }
func (r *TimeRange) Max() ion.Datum     { return ion.Timestamp(r.max) }
func (r *TimeRange) MinTime() date.Time { return r.min }
func (r *TimeRange) MaxTime() date.Time { return r.max }

func (r *TimeRange) Union(t *TimeRange) {
	r.min, r.max = timeUnion(t.min, t.max, r.min, r.max)
}

func timeUnion(min1, max1, min2, max2 date.Time) (min, max date.Time) {
	if min1.Before(min2) {
		min = min1
	} else {
		min = min2
	}
	if max1.After(max2) {
		max = max1
	} else {
		max = max2
	}
	return min, max
}

func pathless(a, b []string) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := range a[:n] {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return len(a) < len(b)
}

func sortByPath(lst []*TimeRange) {
	slices.SortFunc(lst, func(left, right *TimeRange) bool {
		return pathless(left.path, right.path)
	})
}

func (t *TimeRange) copy() *TimeRange {
	return &TimeRange{path: t.path, min: t.min, max: t.max}
}

func copyTimeRanges(lst []*TimeRange) []*TimeRange {
	out := make([]*TimeRange, len(lst))
	for i := range out {
		out[i] = lst[i].copy()
	}
	return out
}

// union unions the results from b into a
// and returns the mutated slice
// (the result is guaranteed not to alias b)
func union(a, b []*TimeRange) []*TimeRange {
	sortByPath(a)
	sortByPath(b)
	pos := 0
	max := len(a) - 1
	for i := range b {
		if pos > max {
			a = append(a, b[i:]...)
			break
		}
		bpath := b[i].path
		apath := a[pos].path
		// search for b <= a
		for pathless(apath, bpath) && pos < max {
			pos++
			apath = a[pos].path
		}
		if slices.Equal(apath, bpath) {
			a[pos].Union(b[i])
		} else {
			a = append(a, b[i].copy())
		}
	}
	sortByPath(a) // make results deterministic
	return a
}

func (b *Blockdesc) merge(from *Blockdesc) {
	b.Chunks += from.Chunks
	b.Ranges = toRanges(
		union(
			toTimeRanges(b.Ranges),
			toTimeRanges(from.Ranges),
		))
}

func collectRanges(t *Trailer) [][]string {
	var out [][]string
	for i := range t.Blocks {
	rangeloop:
		for j := range t.Blocks[i].Ranges {
			p := t.Blocks[i].Ranges[j].Path()
			// FIXME: don't do polynomial-time comparison here :o
			for k := range out {
				if slices.Equal(out[k], p) {
					continue rangeloop
				}
			}
			out = append(out, p)
		}
	}
	return out
}
