//go:build wangshu_oracle_cgo && cgo

/*
 * lua515.c -- single-translation-unit build of the vendored official
 * Lua 5.1.5 library (core + stdlib, no standalone mains).
 *
 * Follows the upstream etc/all.c precedent: defining luaall_c makes
 * luaconf.h mark all internal functions static (LUAI_FUNC), so the
 * whole library collapses into this one translation unit and leaks no
 * internal symbols into the embedding binary. Only the public LUA_API
 * surface (lua_*, luaL_*) is exported, which is exactly what shim.c
 * consumes.
 *
 * The vendored sources under _lua515/ are byte-identical to the
 * upstream tarball (see _lua515/README for origin + sha256); all
 * configuration happens here and in the cgo CFLAGS, never by editing
 * vendored files.
 */

#define luaall_c

/*
 * Pre-include every libc header the Lua sources use, BEFORE any .c
 * below: ldebug.h defines a getline(f,pc) macro that would otherwise
 * clobber POSIX stdio.h's getline declaration when a later .c file
 * includes stdio.h (single-TU-only conflict; upstream compiles these
 * files separately). Pre-inclusion means every system header is fully
 * processed before the macro exists. Vendored sources stay unmodified.
 */
#include <ctype.h>
#include <errno.h>
#include <locale.h>
#include <math.h>
#include <setjmp.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#if defined(LUA_USE_POSIX)
#include <unistd.h>
#endif

#include "_lua515/src/lapi.c"
#include "_lua515/src/lcode.c"
#include "_lua515/src/ldebug.c"
#include "_lua515/src/ldo.c"
#include "_lua515/src/ldump.c"
#include "_lua515/src/lfunc.c"
#include "_lua515/src/lgc.c"
#include "_lua515/src/llex.c"
#include "_lua515/src/lmem.c"
#include "_lua515/src/lobject.c"
#include "_lua515/src/lopcodes.c"
#include "_lua515/src/lparser.c"
#include "_lua515/src/lstate.c"
#include "_lua515/src/lstring.c"
#include "_lua515/src/ltable.c"
#include "_lua515/src/ltm.c"
#include "_lua515/src/lundump.c"
#include "_lua515/src/lvm.c"
#include "_lua515/src/lzio.c"

#include "_lua515/src/lauxlib.c"
#include "_lua515/src/lbaselib.c"
#include "_lua515/src/ldblib.c"
#include "_lua515/src/liolib.c"
#include "_lua515/src/linit.c"
#include "_lua515/src/lmathlib.c"
#include "_lua515/src/loadlib.c"
#include "_lua515/src/loslib.c"
#include "_lua515/src/lstrlib.c"
#include "_lua515/src/ltablib.c"
