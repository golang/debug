package main

// Large is an object that (since Go 1.22) is allocated in a span that has a
// non-nil largeType field. Meaning it must be (>maxSmallSize-mallocHeaderSize).
// At the time of writing this is (32768 - 8).
type Large struct {
	ptr *uint8 // Object must contain a pointer to trigger code path.
	arr [32768 - 8]uint8
}

func ping(o *Large) {
	o.ptr = &o.arr[5]
	o.arr[5] = 0xCA
}

func main() {
	var o Large
	go ping(&o)      // Force an escape of o.
	o.arr[14] = 0xDE // Prevent a future smart compiler from allocating o directly on pings stack.

	_ = *(*int)(nil)
}
