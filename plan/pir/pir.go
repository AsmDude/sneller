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

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/SnellerInc/sneller/date"
	"github.com/SnellerInc/sneller/expr"
	"github.com/SnellerInc/sneller/vm"
)

// table is the base type for operations
// that iterate something that produces rows
//
// tables have a built-in Filter so that references
// to stored tables that use the same filter condition
// can be easily compared for caching purposes
// (in other words, the same table with the same filter
// and referenced columns is the same "view," so we can
// cache that view rather than the whole table if it
// ends up getting touched multiple times)
type table struct {
	references []*expr.Path
	// free variable references
	outer      []string
	Filter     expr.Node
	Bind       string
	star       bool // this table is referenced via '*'
	haveParent bool // free variable references are allowed
}

// strip a path that has been determined
// to resolve to a reference to this table
func (t *table) strip(p *expr.Path) error {
	if t.Bind == "" {
		return nil
	} else if p.First != t.Bind {
		if !t.haveParent || p.Rest != nil {
			return errorf(p, "reference to undefined variable %q", p)
		}
		// this is *definitely* a free variable
		t.outer = append(t.outer, p.First)
		return nil
	}
	d, ok := p.Rest.(*expr.Dot)
	if !ok {
		return errorf(p, "cannot compute %s on table %s", p.Rest, t.Bind)
	}
	p.First = d.Field
	p.Rest = d.Rest
	t.references = append(t.references, p)
	return nil
}

type IterTable struct {
	table
	// free is the set of free variables
	// within this trace that ostensibly
	// reference this table; they may actually
	// be correlated with a parent trace!
	free        []string
	Table       *expr.Table
	Schema      expr.Hint
	TimeRanger  TimeRanger
	Partitioned bool
}

func (i *IterTable) rewrite(rw func(expr.Node, bool) expr.Node) {
	i.Table = rw(i.Table, false).(*expr.Table)
	if i.Filter != nil {
		i.Filter = rw(i.Filter, true)
	}
}

func (i *IterTable) timeRange(p *expr.Path) (min, max date.Time, ok bool) {
	tbl, ok := i.Table.Expr.(*expr.Path)
	if !ok || i.TimeRanger == nil {
		return date.Time{}, date.Time{}, false
	}
	return i.TimeRanger.TimeRange(tbl, p)
}

// Wildcard returns true if the table
// is referenced by the '*' operator
// (in other words, if all column bindings
// in the table are live at some point in the program)
func (i *IterTable) Wildcard() bool {
	return i.star
}

// References returns all of the references
// to the table. Note that if IterTable.Wildcard
// returns true, then any binding in the table
// could be referenced.
func (i *IterTable) References() []*expr.Path {
	return i.references
}

func (i *IterTable) get(x string) (Step, expr.Node) {
	if x == "*" {
		i.table.star = true
		return i, nil
	}
	result := i.Table.Result()
	if result == "" || result == x {
		return i, i.Table
	}
	i.free = append(i.free, x)
	return i, nil
}

func (i *IterTable) describe(dst io.Writer) {
	prefix := "ITERATE"
	if i.Partitioned {
		prefix = "ITERATE PART"
	}
	if i.Filter == nil {
		fmt.Fprintf(dst, "%s %s\n", prefix, expr.ToString(i.Table))
	} else {
		fmt.Fprintf(dst, "%s %s WHERE %s\n", prefix, expr.ToString(i.Table), expr.ToString(i.Filter))
	}
}

func (i *IterTable) parent() Step     { return nil }
func (i *IterTable) setparent(s Step) { panic("IterTable cannot set parent") }

type IterValue struct {
	parented
	table
	Value expr.Node // the expression to be iterated

	liveat     []expr.Binding
	liveacross []expr.Binding
}

func bindstr(bind []expr.Binding) string {
	var out strings.Builder
	for i := range bind {
		if i != 0 {
			out.WriteString(", ")
		}
		out.WriteString(expr.ToString(&bind[i]))
	}
	return out.String()
}

func (i *IterValue) describe(dst io.Writer) {
	if i.Filter == nil {
		fmt.Fprintf(dst, "ITERATE FIELD %s (ref: [%s], live: [%s])\n", expr.ToString(i.Value), bindstr(i.liveat), bindstr(i.liveacross))
	} else {
		fmt.Fprintf(dst, "ITERATE FIELD %s WHERE %s (ref: [%v], live: [%v])\n", expr.ToString(i.Value), expr.ToString(i.Filter), bindstr(i.liveat), bindstr(i.liveacross))
	}
}

func (i *IterValue) rewrite(rw func(expr.Node, bool) expr.Node) {
	i.Value = rw(i.Value, false)
	if i.Filter != nil {
		i.Filter = rw(i.Filter, true)
	}
}

// Wildcard returns whether the value is referenced
// via the '*' operator
// (see also: IterTable.Wildcard)
func (i *IterValue) Wildcard() bool {
	return i.star
}

// References returns the references to this value
func (i *IterValue) References() []*expr.Path {
	return i.references
}

type parented struct {
	par Step
}

func (p *parented) parent() Step     { return p.par }
func (p *parented) setparent(s Step) { p.par = s }

// default behavior for get() for parented nodes
func (p *parented) get(x string) (Step, expr.Node) {
	return p.par.get(x)
}

type binds struct {
	bind []expr.Binding
}

func (i *IterValue) get(x string) (Step, expr.Node) {
	if x == "*" {
		i.table.star = true
		// don't return; the '*'
		// captures the upstream values
		// as well...
	} else if x == i.Bind {
		return i, i.Value
	}
	return i.par.get(x)
}

type Step interface {
	parent() Step
	setparent(Step)
	get(string) (Step, expr.Node)
	describe(dst io.Writer)
	rewrite(func(expr.Node, bool) expr.Node)
}

// Input returns the input to a Step
func Input(s Step) Step {
	return s.parent()
}

// UnionMap represents a terminal
// query Step that unions the results
// of parallel invocations of the Child
// subquery operation.
type UnionMap struct {
	// Inner is the table that needs
	// to be partitioned into the child
	// subquery.
	Inner *IterTable
	// Child is the sub-query that is
	// applied to the partitioned table.
	Child *Trace
}

func (u *UnionMap) parent() Step   { return nil }
func (u *UnionMap) setparent(Step) { panic("cannot UnionMap.setparent()") }

func (u *UnionMap) get(x string) (Step, expr.Node) {
	results := u.Child.FinalBindings()
	for i := len(results) - 1; i >= 0; i-- {
		if results[i].Result() == x {
			return u, results[i].Expr
		}
	}
	return nil, nil
}

func (u *UnionMap) describe(dst io.Writer) {
	var buf bytes.Buffer
	u.Child.Describe(&buf)
	inner := buf.Bytes()
	if inner[len(inner)-1] == '\n' {
		inner = inner[:len(inner)-1]
	}
	inner = bytes.Replace(inner, []byte{'\n'}, []byte{'\n', '\t'}, -1)
	io.WriteString(dst, "UNION MAP ")
	io.WriteString(dst, expr.ToString(u.Inner.Table))
	io.WriteString(dst, " (\n\t")
	dst.Write(inner)
	io.WriteString(dst, ")\n")
}

func (u *UnionMap) rewrite(func(expr.Node, bool) expr.Node) {}

type Filter struct {
	parented
	Where expr.Node
}

func (f *Filter) rewrite(rw func(expr.Node, bool) expr.Node) {
	f.Where = rw(f.Where, true)
}

func (f *Filter) describe(dst io.Writer) {
	fmt.Fprintf(dst, "FILTER %s\n", expr.ToString(f.Where))
}

type Distinct struct {
	parented
	Columns []expr.Node
}

func toStrings(in []expr.Node) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = expr.ToString(in[i])
	}
	return out
}

func (d *Distinct) describe(dst io.Writer) {
	fmt.Fprintf(dst, "FILTER DISTINCT %s\n", toStrings(d.Columns))
}

func (d *Distinct) rewrite(rw func(expr.Node, bool) expr.Node) {
	for i := range d.Columns {
		d.Columns[i] = rw(d.Columns[i], false)
	}
}

func (d *Distinct) clone() *Distinct {
	return &Distinct{Columns: d.Columns}
}

// although the core Distinct implementation technically
// passes through all bindings, semantically only the distinct
// columns are available, so don't allow any references to
// parent variables here
func (d *Distinct) get(x string) (Step, expr.Node) {
	for i := range d.Columns {
		if expr.IsIdentifier(d.Columns[i], x) {
			return d, d.Columns[i]
		}
	}
	return nil, nil
}

func (b *Bind) Bindings() []expr.Binding {
	return b.bind
}

// Bind is a collection of transformations
// that produce a set of output bindings from
// the current binding environment
type Bind struct {
	parented
	binds
	complete bool
	star     bool // referenced by '*'
}

func (b *Bind) rewrite(rw func(expr.Node, bool) expr.Node) {
	for i := range b.bind {
		b.bind[i].Expr = rw(b.bind[i].Expr, false)
	}
}

func (b *Bind) describe(dst io.Writer) {
	io.WriteString(dst, "PROJECT ")
	for i := range b.binds.bind {
		if i != 0 {
			io.WriteString(dst, ", ")
		}
		io.WriteString(dst, expr.ToString(&b.binds.bind[i]))
	}
	io.WriteString(dst, "\n")
}

func (b *Bind) get(x string) (Step, expr.Node) {
	if x == "*" {
		b.star = true
		return b, nil
	}
	for i := len(b.bind) - 1; i >= 0; i-- {
		if b.bind[i].Result() == x {
			return b, b.bind[i].Expr
		}
	}
	if !b.complete {
		return b.parent().get(x)
	}
	return nil, nil
}

// Aggregate is an aggregation operation
// that produces a new set of bindings
type Aggregate struct {
	parented
	Agg vm.Aggregation
	// GroupBy is nil for normal
	// aggregations, or non-nil
	// when the aggregation is formed
	// on multiple columns
	//
	// note that the groups form part
	// of the binding set
	GroupBy []expr.Binding

	complete bool
}

func (a *Aggregate) describe(dst io.Writer) {
	if a.GroupBy == nil {
		fmt.Fprintf(dst, "AGGREGATE %s\n", a.Agg)
	} else {
		fmt.Fprintf(dst, "AGGREGATE %s BY %s\n", a.Agg, vm.Selection(a.GroupBy))
	}
}

func (a *Aggregate) rewrite(rw func(expr.Node, bool) expr.Node) {
	for i := range a.Agg {
		a.Agg[i].Expr = rw(a.Agg[i].Expr, false).(*expr.Aggregate)
	}
	for i := range a.GroupBy {
		a.GroupBy[i].Expr = rw(a.GroupBy[i].Expr, false)
	}
}

func (a *Aggregate) get(x string) (Step, expr.Node) {
	for i := len(a.Agg) - 1; i >= 0; i-- {
		if a.Agg[i].Result == x {
			return a, a.Agg[i].Expr
		}
	}
	for i := len(a.GroupBy) - 1; i >= 0; i-- {
		if a.GroupBy[i].Result() == x {
			return a, a.GroupBy[i].Expr
		}
	}
	if !a.complete {
		return a.parent().get(x)
	}
	// aggregates do not preserve the input binding set
	return nil, nil
}

type Order struct {
	parented
	Columns []expr.Order
}

func (o *Order) clone() *Order {
	return &Order{Columns: o.Columns}
}

func (o *Order) describe(dst io.Writer) {
	io.WriteString(dst, "ORDER BY ")
	for i := range o.Columns {
		if i != 0 {
			io.WriteString(dst, ", ")
		}
		io.WriteString(dst, expr.ToString(&o.Columns[i]))
	}
	io.WriteString(dst, "\n")
}

func (o *Order) rewrite(rw func(expr.Node, bool) expr.Node) {
	for i := range o.Columns {
		o.Columns[i].Column = rw(o.Columns[i].Column, false)
	}
}

type Limit struct {
	parented
	Count  int64
	Offset int64
}

func (l *Limit) describe(dst io.Writer) {
	if l.Offset == 0 {
		fmt.Fprintf(dst, "LIMIT %d\n", l.Count)
		return
	}
	fmt.Fprintf(dst, "LIMIT %d OFFSET %d\n", l.Count, l.Offset)
}

func (l *Limit) rewrite(func(expr.Node, bool) expr.Node) {}

// OutputPart writes output rows into
// a single part and returns a row like
//   {"part": "path/to/packed-XXXXX.ion.zst"}
type OutputPart struct {
	Basename string
	parented
}

func (o *OutputPart) describe(dst io.Writer) {
	fmt.Fprintf(dst, "OUTPUT PART %s\n", o.Basename)
}

func (o *OutputPart) get(x string) (Step, expr.Node) {
	if x == "part" {
		return o, nil
	}
	// NOTE: this would be problematic
	// if Output* nodes were inserted
	// before optimization, as the input
	// fields wouldn't be marked as live
	return nil, nil
}

func (o *OutputPart) rewrite(func(expr.Node, bool) expr.Node) {}

// OutputIndex is a step that takes the "part" field
// of incoming rows and constructs an index out of them,
// returning a single row like
//   {"table_name": "basename-XXXXXX"}
type OutputIndex struct {
	Basename string
	parented
}

func (o *OutputIndex) describe(dst io.Writer) {
	fmt.Fprintf(dst, "OUTPUT INDEX %s\n", o.Basename)
}

func (o *OutputIndex) get(x string) (Step, expr.Node) {
	if x == "table_name" {
		return o, nil
	}
	// see comment in OutputPart.get
	return nil, nil
}

func (o *OutputIndex) rewrite(func(expr.Node, bool) expr.Node) {}

// NoOutput is a dummy input of 0 rows.
type NoOutput struct{}

func (n NoOutput) describe(dst io.Writer) {
	io.WriteString(dst, "NO OUTPUT\n")
}

func (n NoOutput) rewrite(func(expr.Node, bool) expr.Node) {}

func (n NoOutput) get(x string) (Step, expr.Node) { return nil, nil }

func (n NoOutput) parent() Step { return nil }

func (n NoOutput) setparent(Step) { panic("NoOutput.setparent") }

// DummyOutput is a dummy input of one record, {}
type DummyOutput struct{}

func (d DummyOutput) rewrite(func(expr.Node, bool) expr.Node) {}
func (d DummyOutput) get(x string) (Step, expr.Node)          { return nil, nil }
func (d DummyOutput) parent() Step                            { return nil }
func (d DummyOutput) setparent(Step)                          { panic("DummyOutput.setparent") }

func (d DummyOutput) describe(dst io.Writer) {
	io.WriteString(dst, "[{}]\n")
}

func (l *Limit) clone() *Limit {
	return &Limit{Count: l.Count, Offset: l.Offset}
}

// Trace is a linear sequence
// of physical plan operators.
// Traces are arranged in a tree,
// where each Trace depends on the
// execution of zero or more children
// (see Inputs).
type Trace struct {
	// If this trace is not a root trace,
	// then Parent will be a trace that
	// has this trace as one of its inputs.
	Parent *Trace
	// Inputs are traces that produce
	// results that are necessary in order
	// to execute this trace.
	// The results of input traces
	// are available to this trace
	// through the SCALAR_REPLACEMENT(index)
	// and IN_REPLACEMENT(index) expressions.
	// The traces in Input may be executed
	// in any order.
	Inputs []*Trace

	top   Step
	cur   Step
	scope map[*expr.Path]scopeinfo
	err   []error

	// final is the most recent
	// complete set of bindings
	// produced by an expression
	final []expr.Binding
}

func (b *Trace) Begin(f *expr.Table, e Env) {
	it := &IterTable{Table: f}
	it.haveParent = b.Parent != nil
	if f.Explicit() {
		it.Bind = f.Result()
	}
	if s, ok := e.(Schemer); ok {
		it.Schema = s.Schema(f.Expr)
	}
	it.TimeRanger, _ = e.(TimeRanger)
	b.top = it
}

func (b *Trace) beginUnionMap(src *Trace, table *IterTable) {
	// we know that the result of a
	// parallelized query ought to be
	// the same as a non-parallelized query,
	// so we can populate the binding information
	// immediately
	infinal := src.FinalBindings()
	final := make([]expr.Binding, len(infinal))
	copy(final, infinal)
	b.scope = src.scope
	table.Partitioned = true
	b.top = &UnionMap{Child: src, Inner: table}
	b.final = final
}

func (b *Trace) push() error {
	if b.err != nil {
		return b.combine()
	}
	b.cur.setparent(b.top)
	b.top, b.cur = b.cur, nil
	return nil
}

// Where pushes a filter to the expression stack
func (b *Trace) Where(e expr.Node) error {
	f := &Filter{Where: e}
	f.setparent(b.top)
	b.cur = f
	expr.Walk(b, e)
	if err := b.Check(e); err != nil {
		return err
	}
	return b.push()
}

// Iterate pushes an implicit iteration to the stack
func (b *Trace) Iterate(bind *expr.Binding) error {
	iv := &IterValue{Value: bind.Expr}
	iv.Bind = bind.Result()
	// walk with the current scope
	// set to the parent scope; we don't
	// introduce any bindings here that
	// are visible within this node itself
	b.cur = b.top
	expr.Walk(b, iv.Value)
	b.cur = iv

	b.final = append(b.final, *bind)
	return b.push()
}

// Distinct takes a sets of bindings
// and produces only distinct sets of output tuples
// of the given variable bindings
func (b *Trace) Distinct(bind []expr.Binding) error {
	b.cur = b.top
	di := &Distinct{}
	for i := range bind {
		expr.Walk(b, bind[i].Expr)
		if b.err != nil {
			return b.combine()
		}
		di.Columns = append(di.Columns, bind[i].Expr)
	}
	b.final = bind
	b.cur = di
	return b.push()
}

func (b *Trace) BindStar() error {
	b.top.get("*")
	return nil
}

// Bind pushes a set of variable bindings to the stack
func (b *Trace) Bind(bind []expr.Binding) error {
	bi := &Bind{}
	bi.complete = false
	bi.setparent(b.top)
	b.cur = bi

	// walk for each binding introduced,
	// then add it to the current binding set
	for i := range bind {
		expr.Walk(b, bind[i].Expr)
		if b.err != nil {
			return b.combine()
		}
		bi.bind = append(bi.bind, bind[i])
	}
	for i := range bind {
		if err := b.Check(bind[i].Expr); err != nil {
			return err
		}
	}
	// clobber the current binding set
	bi.complete = true
	b.final = bi.bind
	return b.push()
}

// Aggregate pushes an aggregation to the stack
func (b *Trace) Aggregate(agg vm.Aggregation, groups []expr.Binding) error {
	ag := &Aggregate{}
	ag.setparent(b.top)
	ag.complete = false
	b.cur = ag
	var bind []expr.Binding
	for i := range groups {
		expr.Walk(b, groups[i].Expr)
		if b.err != nil {
			return b.combine()
		}
		ag.GroupBy = append(ag.GroupBy, groups[i])
		bind = append(bind, groups[i])
	}
	for i := range agg {
		expr.Walk(b, agg[i].Expr)
		ag.Agg = append(ag.Agg, agg[i])
		bind = append(bind, expr.Bind(agg[i].Expr, agg[i].Result))
	}
	for i := range agg {
		if err := b.Check(ag.Agg[i].Expr); err != nil {
			return err
		}
	}
	ag.complete = true
	b.final = bind
	return b.push()
}

// Order pushes an ordering to the stack
func (b *Trace) Order(cols []expr.Order) error {
	// ... now the variable references should be correct
	b.cur = b.top
	for i := range cols {
		expr.Walk(b, cols[i].Column)
	}
	b.cur = &Order{Columns: cols}
	return b.push()
}

// LimitOffset pushes a limit operation to the stack
func (b *Trace) LimitOffset(limit, offset int64) error {
	l := &Limit{Count: limit, Offset: offset}
	// no walking here because
	// Limit doesnt include any
	// meaningful expressions
	b.cur = l
	return b.push()
}

// Into handles the INTO clause by pushing
// the appropriate OutputIndex and OutputPart nodes.
func (b *Trace) Into(basepath string) {
	op := &OutputPart{Basename: basepath}
	op.setparent(b.top)
	b.add(expr.Identifier("part"), op, nil)
	oi := &OutputIndex{Basename: basepath}
	oi.setparent(op)
	b.top = oi
	tblname := expr.Identifier("table_name")
	result := expr.String(path.Base(basepath))
	b.add(tblname, oi, result)
	final := expr.Bind(result, "table_name")
	b.final = []expr.Binding{final}
}

// FinalBindings returns the set of output bindings,
// or none if they could not be computed
func (b *Trace) FinalBindings() []expr.Binding {
	return b.final
}

// FinalTypes returns the computed ion type sets
// of the output bindings. Each element of the
// returned slice corresponds with the same index
// in the return value of Builder.FinalBindings
//
// Note that the return value may be nil if the
// query does not produce a know (finite) result-set
func (b *Trace) FinalTypes() []expr.TypeSet {
	out := make([]expr.TypeSet, len(b.final))
	for i := range b.final {
		out[i] = b.TypeOf(b.final[i].Expr)
	}
	return out
}

// Final returns the final step of the query.
// The caller can use Input to walk the inputs
// to each step up to the first input step.
func (b *Trace) Final() Step {
	return b.top
}

// String implements fmt.Stringer.
func (b *Trace) String() string {
	if b == nil {
		return "<nil>"
	}
	var sb strings.Builder
	b.Describe(&sb)
	return sb.String()
}

// Describe writes a plain-text representation
// of b to dst. The output of this representation
// is purely for diagnostic purposes; it cannot
// be deserialized back into a trace.
func (b *Trace) Describe(dst io.Writer) {
	var tmp bytes.Buffer
	for i := range b.Inputs {
		io.WriteString(dst, "WITH (\n\t")
		tmp.Reset()
		b.Inputs[i].Describe(&tmp)
		inner := bytes.Replace(tmp.Bytes(), []byte{'\n'}, []byte{'\n', '\t'}, -1)
		inner = inner[:len(inner)-1] // chomp \t on last entry
		dst.Write(inner)
		fmt.Fprintf(dst, ") AS REPLACEMENT(%d)\n", i)
	}
	var describe func(s Step)
	describe = func(s Step) {
		if p := s.parent(); p != nil {
			describe(p)
		}
		s.describe(dst)
	}
	describe(b.top)
}

// conjunctions returns the list of top-level
// conjunctions from a logical expression
// by appending the results to 'lst'
//
// this is used for predicate pushdown so that
//   <a> AND <b> AND <c>
// can be split and evaluated as early as possible
// in the query-processing pipeline
func conjunctions(e expr.Node, lst []expr.Node) []expr.Node {
	a, ok := e.(*expr.Logical)
	if !ok || a.Op != expr.OpAnd {
		return append(lst, e)
	}
	return conjunctions(a.Left, conjunctions(a.Right, lst))
}

// conjoinAll does the inverse of conjunctions,
// returning the given expressions joined with AND.
//
// NOTE: conjunctions(x AND y AND z) returns [z, x, y],
// so conjoinAll(x, y, z) returns "z AND y AND x".
func conjoinAll(x []expr.Node, scope *Trace) expr.Node {
	var node expr.Node
	for i := range x {
		node = conjoin(x[i], node, scope)
	}
	if node != nil {
		node = expr.SimplifyLogic(node, scope)
	}
	return node
}

func (b *Trace) Rewrite(rw expr.Rewriter) {
	inner := func(e expr.Node, _ bool) expr.Node {
		return expr.Rewrite(rw, e)
	}
	for cur := b.top; cur != nil; cur = cur.parent() {
		cur.rewrite(inner)
	}
}
