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

package ion

import (
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
)

// Symtab is an ion symbol table
type Symtab struct {
	interned []string       // symbol -> string lookup
	toindex  map[string]int // string -> symbol lookup
}

// Reset resets a symbol table
// so that it no longer contains
// any symbols (except for the ion
// pre-defined symbols).
func (s *Symtab) Reset() {
	// NOTE: we could probably
	// get away with not deleting
	// s.toindex and instead deleting
	// its entries in order to avoid
	// it being re-allocated.
	// Not sure if deleting the entries
	// or re-allocating a new map is faster.
	s.clear()
}

// Get gets the string associated
// with the given interned symbol,
// or returns the empty string
// when there is no symbol with
// the given association.
func (s *Symtab) Get(x Symbol) string {
	if int(x) < len(systemsyms) {
		return systemsyms[x]
	}
	id := int(x) - len(systemsyms)
	if id < len(s.interned) {
		return s.interned[id]
	}
	return ""
}

// MaxID returns the total number of
// interned symbols. Note that ion
// defines ten symbols that are automatically
// interned, so an "empty" symbol table
// has MaxID() of 10.
func (s *Symtab) MaxID() int {
	return len(systemsyms) + len(s.interned)
}

// InternBytes is identical to Intern,
// except that it accepts a []byte instead of
// a string as an argument.
func (s *Symtab) InternBytes(buf []byte) Symbol {
	// We are relying on the fact that
	// the Go compiler knows it can elide
	// allocation when string([]byte) is
	// called directly as part of a map access.
	if i, ok := system2id[string(buf)]; ok {
		return Symbol(i)
	}
	if s.toindex != nil {
		i, ok := s.toindex[string(buf)]
		if ok {
			return Symbol(len(systemsyms) + i)
		}
	} else {
		s.toindex = make(map[string]int)
	}
	s.toindex[string(buf)] = len(s.interned)
	s.interned = append(s.interned, string(buf))
	return Symbol(len(systemsyms) + len(s.interned) - 1)
}

// Intern interns the given string
// if it is not already interned
// and returns the associated Symbol
func (s *Symtab) Intern(x string) Symbol {
	if i, ok := system2id[x]; ok {
		return Symbol(i)
	}
	if s.toindex != nil {
		i, ok := s.toindex[x]
		if ok {
			return Symbol(len(systemsyms) + i)
		}
	} else {
		s.toindex = make(map[string]int)
	}
	s.toindex[x] = len(s.interned)
	s.interned = append(s.interned, x)
	return Symbol(len(systemsyms) + len(s.interned) - 1)
}

// Symbolize returns the symbol associated
// with the string 'x' in the symbol table,
// or (0, false) if the string has not been
// interned.
func (s *Symtab) Symbolize(x string) (Symbol, bool) {
	if i, ok := system2id[x]; ok {
		return Symbol(i), true
	}
	if s.toindex != nil {
		i, ok := s.toindex[x]
		if ok {
			return Symbol(len(systemsyms) + i), true
		}
	}
	return 0, false
}

// Equal checks if two symtabs are equal.
func (s *Symtab) Equal(o *Symtab) bool {
	return reflect.DeepEqual(s, o)
}

// CloneInto performs a deep copy
// of s into o.
func (s *Symtab) CloneInto(o *Symtab) {
	if cap(o.interned) >= len(s.interned) {
		o.interned = o.interned[:len(s.interned)]
	} else {
		o.interned = make([]string, len(s.interned))
	}
	copy(o.interned, s.interned)
	if o.toindex == nil {
		o.toindex = make(map[string]int, len(s.toindex))
	} else {
		for k := range o.toindex {
			delete(o.toindex, k)
		}
	}
	for i := range o.interned {
		o.toindex[o.interned[i]] = i
	}
}

// these symbols are predefined
var systemsyms = []string{
	"$0",
	"$ion",
	"$ion_1_0",
	"$ion_symbol_table",
	"name",
	"version",
	"imports",
	"symbols",
	"max_id",
	"$ion_shared_symbol_table",
}

const (
	symbolImports              = 6
	symbolSymbols              = 7
	dollarIonSymbolTable       = 3
	dollarIonSharedSymbolTable = 9
)

var system2id map[string]int

func init() {
	system2id = make(map[string]int, len(systemsyms))
	for i := range systemsyms {
		system2id[systemsyms[i]] = i
	}
}

// MinimumID returns the lowest ID
// that a string could be symbolized as.
//
// System symbols have IDs less than 10;
// all other symbols have and ID of at least 10.
func MinimumID(str string) int {
	i, ok := system2id[str]
	if !ok {
		return len(systemsyms)
	}
	return i
}

// IsBVM returns whether or not
// the next 4 bytes of the message
// are a 4-byte ion BVM marker.
func IsBVM(buf []byte) bool {
	if len(buf) < 4 {
		return false
	}
	// BVM begins with 0xe0 and ends with 0xea
	word := binary.LittleEndian.Uint32(buf)
	return word&0xff0000ff == 0xea0000e0
}

func (s *Symtab) clear() {
	s.interned = s.interned[:0]
	s.toindex = nil
}

func start(x []byte) []byte {
	if len(x) > 8 {
		x = x[:8]
	}
	return x
}

// Unmarshal unmarshals a symbol
// table from 'src' into 's'.
// If 'src' begins with a BVM
// (see IsBVM), then any contents
// of the symbol table will be cleared
// before interning the new symbol values.
// Otherwise, the new symbols will be
// interned with IDs above the presently-interned
// symbols.
//
// BUGS: Support for ion "shared" symbol tables
// is not yet implemented.
func (s *Symtab) Unmarshal(src []byte) ([]byte, error) {
	if IsBVM(src) {
		s.clear()
		src = src[4:]
	}
	if len(src) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if t := TypeOf(src); t != AnnotationType {
		return nil, bad(t, AnnotationType, "Symtab.Unmarshal")
	}
	if len(src) < SizeOf(src) {
		return nil, fmt.Errorf("Symtab.Unmarshal: len(src)=%d, SizeOf(src)=%d", len(src), SizeOf(src))
	}
	body, rest := Contents(src)
	if body == nil {
		return nil, fmt.Errorf("Symtab.Unmarshal: Contents(%x)==nil", start(src))
	}
	// skip annotation_length field
	fields, body, err := ReadLabel(body)
	if err != nil {
		return nil, err
	}
	if fields != 1 {
		return nil, fmt.Errorf("%d annotations?", fields)
	}
	// read struct field
	sym, body, err := ReadLabel(body)
	if err != nil {
		return nil, err
	}
	if sym != dollarIonSymbolTable {
		// FIXME: add support for shared symbol tables
		return nil, fmt.Errorf("first annotation field not $ion_symbol_table")
	}
	if t := TypeOf(body); t != StructType {
		return nil, bad(t, StructType, "Symtab.Unmarshal (in annotation)")
	}
	if s.toindex == nil {
		s.toindex = make(map[string]int)
	}
	body, _ = Contents(body)
	if body == nil {
		return nil, fmt.Errorf("Symtab.Unmarshal: Contents(structure(%x))==nil", start(body))
	}
	// walk through the body fields
	// and look for 'symbols: [...]'
	// from which we can intern strings
	for len(body) > 0 {
		sym, body, err = ReadLabel(body)
		if err != nil {
			return nil, fmt.Errorf("Symtab.Unmarshal (reading fields): %w", err)
		}
		switch sym {
		case symbolSymbols:
			var lst []byte
			lst, body = Contents(body)
			if lst == nil {
				return nil, fmt.Errorf("Symtab.Unmarshal: Contents(%x)==nil", start(body))
			}
			for len(lst) > 0 {
				var str string
				str, lst, err = ReadString(lst)
				if err != nil {
					return nil, fmt.Errorf("Symtab.Unmarshal (in 'symbols:') %w", err)
				}
				// XXX what is the correct behavior here
				// when a string is interned more than
				// once?
				s.interned = append(s.interned, str)
				if _, ok := s.toindex[str]; !ok {
					s.toindex[str] = len(s.interned) - 1
				}
			}
		default:
			// skip unknown field
			s := SizeOf(body)
			if s < 0 || len(body) < s {
				return nil, fmt.Errorf("Symtab.Unmarshal: skipping field len=%d; len(body)=%d", s, len(body))
			}
			body = body[s:]
		}
	}

	return rest, nil
}

// MarshalPart writes a symbol table to dst
// with all the symbols starting at starting.
// If there are no symbols above starting, then
// MarshalPart does not write any data.
//
// Callers can use a previous result of
// s.MaxID plus MarshalPart to write incremental
// changes to symbol tables to an ion stream.
func (s *Symtab) MarshalPart(dst *Buffer, starting Symbol) {
	s.marshal(dst, starting, false)
}

// Marshal marshals the Symtab into 'dst'
// optionally with a BVM prefix.
//
// If withBVM is false and the symbol table
// is empty, then no data is written to dst.
func (s *Symtab) Marshal(dst *Buffer, withBVM bool) {
	s.marshal(dst, 0, withBVM)
}

func (s *Symtab) marshal(dst *Buffer, starting Symbol, withBVM bool) {
	if withBVM {
		dst.buf = append(dst.buf, 0xe0, 0x01, 0x00, 0xea)
	}
	count := 0
	if int(starting) > len(systemsyms) {
		count = int(starting) - len(systemsyms)
		if count > len(s.interned) {
			count = len(s.interned)
		}
	}
	if count == 0 && !withBVM {
		// no new data; append nothing
		return
	}
	interned := s.interned[count:]
	dst.BeginAnnotation(1)
	// $ion_symbol_table: { symbols: [ ... ] }
	dst.BeginField(dollarIonSymbolTable)
	dst.BeginStruct(-1)
	if !withBVM {
		dst.BeginField(symbolImports)
		dst.WriteSymbol(dollarIonSymbolTable)
	}
	dst.BeginField(symbolSymbols)
	dst.BeginList(-1)
	for i := range interned {
		dst.WriteString(interned[i])
	}
	dst.EndList()
	dst.EndStruct()
	dst.EndAnnotation()
}

// Contains returns true if s is a superset
// of the symbols within inner, and all of
// the symbols in inner have the same symbol
// ID in s.
//
// If x.Contains(y), then x is a semantically
// equivalent substitute for y.
func (s *Symtab) Contains(inner *Symtab) bool {
	if len(inner.interned) > len(s.interned) {
		return false
	}
	for i := range inner.interned {
		if s.interned[i] != inner.interned[i] {
			return false
		}
	}
	return true
}
