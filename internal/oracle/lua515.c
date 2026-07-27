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

/*
 * NaN text normalization (differential-fuzz oracle only).
 *
 * glibc's printf renders a NaN's sign bit, so 0/0 prints "-nan" on this build
 * while wangshu prints "nan". IEEE 754 gives that bit no numeric meaning and
 * both spellings conform, so this is a rendering choice rather than a semantic
 * difference. It cannot usefully be exempted on the comparison side, though:
 * once the sign byte is inside a string it is ordinary data, and '#', '=='.
 * string.sub, '..' and arithmetic move it anywhere -- string.len(0/0) becomes
 * 4 against 3, and string.len(0/0)*100 becomes 400 against 300, with no NaN
 * token left in the output to anchor an exemption to.
 *
 * So it is removed where it is produced. This oracle exists only to be a
 * differential baseline; it is already stubbed for determinism elsewhere
 * (os.time, math.random, table iteration order), and dropping a sign character
 * that carries no value is the same kind of trade.
 *
 * Two rendering paths need it, both routed through lua_number2str, which
 * luaconf.h defines and this single translation unit may override before the
 * vendored sources are included:
 *   - tostring / '..' / io.write on a number (lvm.c luaO_tostring);
 *   - LUA_NUMBER_FMT elsewhere.
 * string.format's %e/%f/%g family calls sprintf directly and is handled by
 * wangshu_fixnan applied in str_format, see the lstrlib.c include below.
 *
 * Vendored sources stay byte-identical (see _lua515/README sha256).
 */
static void wangshu_fixnan_spec (char *s, const char *form) {
  /* Remove the sign from a NaN rendering while preserving the FIELD WIDTH.
   *
   * The buffer alone cannot decide what a leading space is: "%5E" and "% E"
   * both produce a space before the word, but the first is padding to keep and
   * the second is the space-flag sign to drop. So the format spec is passed in
   * and the declared width read from it; the space is a sign exactly when the
   * rendering is wider than the declared width.
   *
   * form is NULL for lua_number2str, which has no flags or width. */
  char *w = NULL;
  size_t len, width = 0;
  int left = 0;
  {
    char *q;
    for (q = s; *q != '\0'; q++) {
      if ((q[0] == 'n' || q[0] == 'N') && (q[1] == 'a' || q[1] == 'A') &&
          (q[2] == 'n' || q[2] == 'N')) { w = q; break; }
    }
  }
  if (w == NULL) return;                 /* not a NaN rendering */
  /* Everything before the word must be padding or one sign. */
  {
    char *q;
    int signs = 0;
    for (q = s; q < w; q++) {
      if (*q == ' ') continue;
      if ((*q == '-' || *q == '+') && signs == 0) { signs++; continue; }
      return;                            /* some other prefix: leave alone */
    }
  }
  if (form != NULL) {
    const char *q = form + 1;            /* skip '%' */
    for (; *q != '\0'; q++) {
      if (*q == '-') { left = 1; continue; }
      if (*q == '+' || *q == ' ' || *q == '#' || *q == '0') continue;
      break;
    }
    for (; *q >= '0' && *q <= '9'; q++) width = width * 10 + (size_t)(*q - '0');
  }
  len = strlen(s);
  /* Strip every sign/pad byte before the word, then re-pad to the declared
   * width. This lands on the same answer for both readings of a leading space,
   * because the width, not the buffer, decides how much padding belongs. */
  {
    size_t wordlen = strlen(w);
    /* MAX_ITEM in lstrlib.c is 512, and a width is at most two digits, so a
     * padded run cannot approach this. Sized against the run INCLUDING its
     * trailing padding: measuring the stripped word instead made every
     * left-justified field wider than the buffer bail out early and keep its
     * sign. */
    char tmp[600];
    size_t i, pad;
    if (wordlen >= sizeof tmp) return;
    memcpy(tmp, w, wordlen + 1);
    /* trailing padding is part of the word run for a left-justified field */
    while (wordlen > 0 && tmp[wordlen - 1] == ' ') tmp[--wordlen] = '\0';
    pad = (width > wordlen) ? width - wordlen : 0;
    if (left) {
      memcpy(s, tmp, wordlen);
      for (i = 0; i < pad; i++) s[wordlen + i] = ' ';
      s[wordlen + pad] = '\0';
    } else {
      for (i = 0; i < pad; i++) s[i] = ' ';
      memcpy(s + pad, tmp, wordlen);
      s[pad + wordlen] = '\0';
    }
  }
  (void)len;
}

static void wangshu_fixnan (char *s) { wangshu_fixnan_spec(s, NULL); }

static void wangshu_number2str (char *s, double n) {
  sprintf(s, LUA_NUMBER_FMT, n);
  wangshu_fixnan(s);
}

#undef lua_number2str
#define lua_number2str(s,n) wangshu_number2str((s), (n))

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
/*
 * string.format's %e/%f/%g family calls sprintf directly rather than going
 * through lua_number2str, so the same normalization is applied by shadowing
 * sprintf for this include only. wangshu_sprintf forwards to the real one and
 * then drops a NaN's sign, which is a no-op for every other conversion because
 * no other rendering can begin with "-nan"/"-NAN".
 *
 * Scoped to lstrlib.c and undefined immediately after: no other vendored file
 * has its sprintf calls rewritten.
 */
static int wangshu_sprintf (char *buf, const char *fmt, ...) {
  int r;
  va_list ap;
  va_start(ap, fmt);
  r = vsprintf(buf, fmt, ap);
  va_end(ap);
  /* Normalize ONLY a float conversion's own output.
   *
   * The shadow covers every sprintf in lstrlib.c, including the %s and %c
   * cases, and those render caller-supplied bytes. Rewriting them would corrupt
   * a user string that merely begins with "nan" -- string.format("%s", "-nan")
   * would come back as "nan", and its length as 3 against wangshu's 4. That is
   * precisely the failure this change exists to remove, so the conversion
   * character is checked first: only e/E/f/g/G renderings can produce a NaN
   * word of their own. */
  {
    const char *q = fmt + strlen(fmt);
    if (q > fmt) {
      char verb = q[-1];
      if (verb == 'e' || verb == 'E' || verb == 'f' ||
          verb == 'g' || verb == 'G')
        wangshu_fixnan_spec(buf, fmt);
    }
  }
  return r;
}

#define sprintf wangshu_sprintf
#include "_lua515/src/lstrlib.c"
#undef sprintf

#include "_lua515/src/ltablib.c"
