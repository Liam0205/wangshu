/* shim.h -- C interface between oracle.go (cgo) and shim.c. */

#ifndef WANGSHU_ORACLE_SHIM_H
#define WANGSHU_ORACLE_SHIM_H

#include <stddef.h>

/* verdict codes: keep in sync with oracle.go Verdict constants. */
#define WANGSHU_ORACLE_OK    0
#define WANGSHU_ORACLE_ERROR 1
#define WANGSHU_ORACLE_LIMIT 2

/*
 * wangshu_oracle_exec runs `prelude` then `src` on a fresh Lua 5.1.5
 * state with a byte-capped allocator (max_alloc) and an instruction-
 * count budget (budget VM instructions; <=0 disables).
 *
 * Returns a verdict code. On OK/ERROR, *out receives the captured
 * output bytes (malloc'd, caller frees via wangshu_oracle_free);
 * on ERROR/LIMIT, *err receives the error message likewise.
 */
int wangshu_oracle_exec(const char *src, size_t src_len,
                        const char *prelude, size_t prelude_len,
                        size_t max_alloc, int budget,
                        char **out, size_t *out_len,
                        char **err, size_t *err_len);

void wangshu_oracle_free(char *p);

#endif /* WANGSHU_ORACLE_SHIM_H */
