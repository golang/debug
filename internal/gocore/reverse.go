// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocore

import (
	"iter"
)

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

// A Link represents a pointer to [Link.Dst].
//
// [Link.DstOff] is non-zero if the pointer is an interior pointer.
//
// [Link.SrcOff] is non-zero if the location of the pointer itself is in the
// interior of the source. The source object itself is not in the Link struct.
// See either the previous Link or the [Root].
//
// An example, assuming objects "src" and "dst" exist:
//
//	src{
//	  a // ...
//	  b // ...
//	  ptr *someType
//	}
//
//	dst{
//	  c // ...
//	  d // ...
//	  someType // ...
//	}
//
// Then the Link representing the connection will be
//
//	Link{
//	  SrcOff: &src.ptr - &src,
//	  Dst:    &dst,
//	  DstOff: &dst.someType - &dst,
//	}
//
// TODO(aktau): How are Roots that _are_ the object we're looking for themselves
// materialized?
type Link struct {
	Dst    Object // Object containing the pointed-to address.
	SrcOff int64  // Offset in the source [Object] or [Root] where this pointer was found. The location of the pointer is p.Addr(source).Add(SrcOff).
	DstOff int64  // Offset into Dst. The pointed-to address is p.Addr(Dst).Add(DstOff).
}

// Reachable returns an iterator that yields a path (slice of [Link]s) for every
// [Root] that refers to obj.
//
// The order in which roots are returned is undefined but deterministic.
// Similarly for multiple paths out of a given root: a shortest path is
// returned. Which one is undefined but deterministic.
//
// The link slice always contains at least one element, representing the link
// between the root and the object.
//
// The link slice (path) is invalidated on every iteration. The elements are
// values, so (e.g.) [slices.Clone] can be used to retain the path.
func (p *Process) Reachable(obj Object) iter.Seq2[*Root, []Link] {
	return func(yield func(*Root, []Link) bool) {
		depth := map[Object]int{}
		depth[obj] = 0
		var path []Link // Path slice is reused over all returned elements.
		q := []Object{obj}
		var stop bool
		for len(q) > 0 && !stop {
			// Follow the reverse pointers Depth-First (FIFO).
			y := q[0]
			q = q[1:]

			p.ForEachReversePtr(y, func(x Object, r *Root, i, j int64) bool {
				if r != nil {
					// Found a root. Reconstruct a path.
					path = path[:0] // Re-use memory.
					path = append(path, Link{
						SrcOff: i, // i: the offset in [r] where the pointer resides.
						Dst:    y,
						DstOff: j, // j: the offset in [y] where the pointer points.
					})

					// Object hierarchy.
					for z := y; z != obj; {
						// Find an edge out of z which goes to an object closer to obj.
						p.ForEachPtr(z, func(i int64, w Object, j int64) bool {
							if d, ok := depth[w]; ok && d < depth[z] {
								path = append(path, Link{
									SrcOff: i, // the offset in [z] where the pointer resides.
									Dst:    w,
									DstOff: j, // the offset in [w] where the pointer points.
								})
								z = w
								return false
							}
							return true
						})
					}

					if !yield(r, path) {
						stop = true
						return false
					}
					return true
				}

				// Reverse pointer to a non-root. Add this object to the work list
				// (unless we've already seen it).
				if _, ok := depth[x]; ok {
					return true
				}
				depth[x] = depth[y] + 1
				q = append(q, x)
				return true
			})
		}
	}
}
