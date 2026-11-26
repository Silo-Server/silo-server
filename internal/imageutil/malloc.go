//go:build !silo_node

package imageutil

// libvips and the codecs under it allocate their pixel buffers with malloc on
// whichever OS thread runs the operation, and Silo runs operations from a
// worker pool sized per core. glibc gives every thread that allocates an
// arena of its own, up to eight per core, and keeps most of what is freed in
// an arena for that arena's next allocation. After the artwork of a library
// of about a thousand files was cached on a 16-thread host, about 90 arenas
// held 4.4 GB the process never used again, against a Go heap of about
// 200 MB. With two arenas and the idle release below, the same work peaked at
// 1.9 GB instead of 5 GB and settled at 0.4 GB, in the same time.
//
// The limit is set from a C constructor because glibc fixes it the first
// time more than eight arenas exist, and by the time a Go package init runs
// the runtime's own threads have taken that many. An operator's
// MALLOC_ARENA_MAX or glibc.malloc.arena_max tunable wins. Other C libraries
// (musl, macOS) are left alone.

/*
#include <stdlib.h>
#include <string.h>
#if defined(__GLIBC__)
#include <malloc.h>

__attribute__((constructor)) static void silo_limit_malloc_arenas(void) {
	const char *tunables = getenv("GLIBC_TUNABLES");
	if (getenv("MALLOC_ARENA_MAX") != NULL ||
	    (tunables != NULL && strstr(tunables, "glibc.malloc.arena_max") != NULL)) {
		return;
	}
	mallopt(M_ARENA_MAX, 2);
}

static void silo_malloc_trim(void) { malloc_trim(0); }
#else
static void silo_malloc_trim(void) {}
#endif
*/
import "C"

// releaseFreeMemory returns the pages malloc holds free to the operating
// system. Without it the arenas keep the memory a burst of image work freed;
// see decodeBudget.
func releaseFreeMemory() {
	C.silo_malloc_trim()
}
