// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocore

func (p *Process) reverseEdges() {
	p.initReverseEdges.Do(func() {
		// First, count the number of edges into each object.
		// This allows for efficient packing of the reverse edge storage.
		cnt := make([]int64, p.nObj+1)
		p.ForEachObject(func(x Object) bool {
			p.ForEachPtr(x, func(_ int64, y Object, _ int64) bool {
				idx, _ := p.findObjectIndex(p.Addr(y))
				cnt[idx]++
				return true
			})
			return true
		})
		p.ForEachRoot(func(r *Root) bool {
			p.ForEachRootPtr(r, func(_ int64, y Object, _ int64) bool {
				idx, _ := p.findObjectIndex(p.Addr(y))
				cnt[idx]++
				return true
			})
			return true
		})

		// Compute cumulative count of all incoming edges up to and including each object.
		var n int64
		for idx, c := range cnt {
			n += c
			cnt[idx] = n
		}

		// Allocate all the storage for the reverse edges.
		p.redge = make([]reverseEdge, n)

		// Add edges to the lists.
		p.ForEachObject(func(x Object) bool {
			p.ForEachPtr(x, func(i int64, y Object, _ int64) bool {
				idx, _ := p.findObjectIndex(p.Addr(y))
				e := cnt[idx]
				e--
				cnt[idx] = e
				p.redge[e] = reverseEdge{addr: p.Addr(x).Add(i)}
				return true
			})
			return true
		})
		p.ForEachRoot(func(r *Root) bool {
			p.ForEachRootPtr(r, func(i int64, y Object, _ int64) bool {
				idx, _ := p.findObjectIndex(p.Addr(y))
				e := cnt[idx]
				e--
				cnt[idx] = e
				p.redge[e] = reverseEdge{root: r, rOff: i}
				return true
			})
			return true
		})
		// At this point, cnt contains the cumulative count of all edges up to
		// but *not* including each object.
		p.ridx = cnt

		// Make root index.
		p.rootIdx = make([]*Root, p.nRoots)
		p.ForEachRoot(func(r *Root) bool {
			p.rootIdx[r.id] = r
			return true
		})
	})
}

// ForEachReversePtr calls fn for all pointers it finds pointing to y.
// It calls fn with:
//
//	the object or root which points to y (exactly one will be non-nil)
//	the offset i in that object or root where the pointer appears.
//	the offset j in y where the pointer points.
//
// If fn returns false, ForEachReversePtr returns immediately.
func (p *Process) ForEachReversePtr(y Object, fn func(x Object, r *Root, i, j int64) bool) {
	p.reverseEdges()

	idx, _ := p.findObjectIndex(p.Addr(y))
	for _, a := range p.redge[p.ridx[idx]:p.ridx[idx+1]] {
		if a.root != nil {
			ptr, _ := p.readRootPtr(a.root, a.rOff)
			if !fn(0, a.root, a.rOff, ptr.Sub(p.Addr(y))) {
				return
			}
		} else {
			// Read pointer, compute offset in y.
			ptr := p.proc.ReadPtr(a.addr)
			j := ptr.Sub(p.Addr(y))

			// Find source of pointer.
			x, i := p.FindObject(a.addr)
			if x != 0 {
				// Source is an object.
				if !fn(x, nil, i, j) {
					return
				}
			}
		}
	}
}
