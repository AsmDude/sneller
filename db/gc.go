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

package db

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"time"

	"github.com/SnellerInc/sneller/date"
	"github.com/SnellerInc/sneller/fsutil"
	"github.com/SnellerInc/sneller/ion/blockfmt"
)

// RemoveFS is an fs.FS with a Remove operation.
type RemoveFS interface {
	fs.FS
	Remove(name string) error
}

var (
	_ RemoveFS = &S3FS{}
	_ RemoveFS = &DirFS{}
)

// DefaultMinimumAge is the default minimum
// age of packed files to be deleted.
const DefaultMinimumAge = 15 * time.Minute

// GCConfig is a configuration for
// garbage collection.
type GCConfig struct {
	// MinimumAge, if non-zero, specifies
	// the minimum age for any objects removed
	// during a garbage-collection pass.
	// Note that objects are only candidates
	// for garbage collection if they are older
	// than the current index *and* not pointed to
	// by the current index, so the MinimumAge requirement
	// is only necessary if it is possible for GC and ingest
	// to run simultaneously. In that case, MinimumAge should be
	// set to some duration longer than any possible ingest cycle.
	MinimumAge time.Duration
	// Logf, if non-nil, is a callback used for logging
	// detailed information regarding GC decisions.
	Logf func(f string, args ...interface{})

	// Precise determines if GC is performed
	// by only deleting objects that have been
	// explicitly marked for deletion.
	Precise bool
}

func (c *GCConfig) logf(f string, args ...interface{}) {
	if c.Logf != nil {
		c.Logf(f, args...)
	}
}

const (
	// this is the pattern we reserve for objects
	// that are generated by ingest and can be garbage collected;
	// perhaps this should be adjusted down the road...
	packedPattern = "packed-*.ion.zst"
	inputsPattern = "inputs-*"
)

// Run calls rfs.Remove(path) for each path
// within the provided database name and table
// that a) has a filename pattern that indicates
// it was packed by Sync, at b) is not pointed to
// by idx.
func (c *GCConfig) Run(rfs RemoveFS, dbname string, idx *blockfmt.Index) error {
	if c.Precise {
		c.preciseGC(rfs, idx)
	}

	// pin relative time to start time,
	// since we don't want to look at
	// anything that has been written since
	// the GC operation actually started
	start := time.Now()
	used := make(map[string]struct{})
	for i := range idx.Contents {
		used[idx.Contents[i].Path] = struct{}{}
	}
	idx.Inputs.EachFile(func(f string) {
		used[f] = struct{}{}
	})
	type spec struct {
		pattern string
		minAge  time.Duration
	}

	packedmin := c.MinimumAge
	if packedmin == 0 {
		packedmin = DefaultMinimumAge
	}
	for _, spc := range []spec{
		// queries can use packed files during execution,
		// so don't delete them until they are fairly old
		{packedPattern, packedmin},
		// inputs can really only be referenced by
		// the index, more-or-less as soon as an index
		// becomes visible, the old inputs can be deleted
		{inputsPattern, 30 * time.Second},
	} {
		walk := func(p string, f fs.File, err error) error {
			if err != nil {
				return err
			}
			if _, ok := used[p]; ok {
				f.Close()
				c.logf("%s is referenced", p)
				return nil
			}
			info, staterr := f.Stat()
			f.Close()
			if staterr != nil {
				c.logf("s: %s", p, staterr)
				if err == nil {
					err = staterr
				}
				return err
			}
			if info.ModTime().After(idx.Created.Time()) {
				// if, due to some kind of synchronization failure,
				// we are running an ingest at the same time that
				// we are runing GC, then new files will be introduced
				// that are not yet pointed to by an index; we shouldn't
				// remove them since they could still be used by a future
				// index
				c.logf("%s: ignoring; newer than index", p)
				return nil
			}
			if spc.minAge != 0 && start.Sub(info.ModTime()) < spc.minAge {
				c.logf("%s: ignoring; does not meet minimum age", p)
				return nil
			}
			if rmerr := rfs.Remove(p); rmerr != nil {
				c.logf("%s/%s: %s", dbname, idx.Name, err)
			} else {
				c.logf("removed %s", p)
			}
			return nil
		}
		err := fsutil.WalkGlob(rfs, "", path.Join("db", dbname, idx.Name, spc.pattern), walk)
		if err != nil {
			return err
		}
	}
	return nil
}

// preciseGC removes expired elements from idx.ToDelete
// and returns true if any items were removed, or otherwise false
func (c *GCConfig) preciseGC(rfs RemoveFS, idx *blockfmt.Index) bool {
	if len(idx.ToDelete) == 0 {
		return false
	}
	// FIXME: just make this heap-ordered
	sort.Slice(idx.ToDelete, func(i, j int) bool {
		return idx.ToDelete[i].Expiry.Before(idx.ToDelete[j].Expiry)
	})
	any := false
	now := date.Now()
	for len(idx.ToDelete) > 0 && idx.ToDelete[0].Expiry.Before(now) {
		err := rfs.Remove(idx.ToDelete[0].Path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			any = true
			idx.ToDelete = idx.ToDelete[1:]
		} else {
			c.logf("deleting ToDelete %q: %s", idx.ToDelete[0].Path, err)
			return any
		}
	}
	return any
}
