package loclist

import (
	"bytes"

	"golang.org/x/debug/third_party/delve/dwarf/godwarf"
)

// Enumerate walks through all of the location list entries for a
// given variable in a given function and enumerates them, returning
// to the client. Note that this function doesn't exist in the delve
// source; it was written here so as to be able to do something
// similar to what's done with DWARF 2 location lists. Here off is the
// offset within the .debug_loclists section containing the start of
// the entries for the function in question, staticBase is the
// start-of-text address for the executable, and debugAddr
// encapsulates the portion of the .debug_addr section containing
// entries for the current compilation unit.
func (rdr *Dwarf5Reader) Enumerate(off int64, staticBase uint64, debugAddr *godwarf.DebugAddr) ([]Entry, error) {
	result := []Entry{}

	it := &loclistsIterator{rdr: rdr, debugAddr: debugAddr, buf: bytes.NewBuffer(rdr.data), staticBase: staticBase}
	it.buf.Next(int(off))

	for it.next() {
		if !it.onRange {
			continue
		}
		e := Entry{it.start, it.end, it.instr}
		result = append(result, e)
	}

	return result, it.err
}
