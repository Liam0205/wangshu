//go:build wangshu_oracle_cgo && cgo

/*
 * shim.c -- process-embedded official Lua 5.1.5 execution shim for
 * differential fuzzing (wangshu vs PUC oracle, in-process).
 *
 * Contract (mirrored by oracle.go):
 *   - one fresh lua_State per exec, closed before returning: zero
 *     state leaks between fuzz inputs;
 *   - capped allocator: a custom lua_Alloc refuses growth beyond
 *     max_alloc bytes, which PUC surfaces as LUA_ERRMEM ("not enough
 *     memory") -- an oracle-resource verdict, not a semantic one;
 *   - instruction budget: LUA_MASKCOUNT hook raises a sentinel error
 *     (ORACLE_LIMIT) after budget instructions, bounding runaway
 *     loops; the hook is inherited by coroutines (lua_newthread
 *     copies hookmask), so budgets bind inside resume too;
 *   - all output capture / stdlib trimming happens in the Lua-text
 *     prelude supplied by the Go side, not here: the shim only
 *     executes chunks and reports a three-state verdict.
 */

#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "_lua515/src/lua.h"
#include "_lua515/src/lauxlib.h"
#include "_lua515/src/lualib.h"

#include "shim.h"

/* Sentinel prefix for shim-imposed limits (instruction budget, output
 * cap). The Go side classifies any error message carrying it as
 * VerdictLimit. The prefix deliberately cannot collide with organic
 * script errors: scripts CAN fake it via error("ORACLE_LIMIT..."),
 * but the prelude runs both sides with the same fake, so a faked
 * limit skips the comparison symmetrically -- no false positive. */
#define ORACLE_LIMIT_SENTINEL "ORACLE_LIMIT"

/* allocator state: byte-accounted cap.  PUC 5.1 reports the old size
 * of every block in osize, so net accounting is exact. */
typedef struct {
    size_t used;
    size_t cap;
} alloc_state;

typedef struct {
    int64_t started_ns;
    int64_t wall_time_ns;
    int remaining;
    int hook_interval;
    const char *limit_reason;
} limit_state;

static int64_t monotonic_ns(void) {
    struct timespec ts;
    if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
        return -1;
    }
    return (int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

static void check_wall_time(limit_state *ls) {
    if (ls->wall_time_ns > 0 && ls->limit_reason == NULL) {
        int64_t now = monotonic_ns();
        if (now < 0) {
            ls->limit_reason = ORACLE_LIMIT_SENTINEL ": monotonic clock unavailable";
        } else if (now - ls->started_ns >= ls->wall_time_ns) {
            ls->limit_reason = ORACLE_LIMIT_SENTINEL ": wall time budget";
        }
    }
}

static void *capped_alloc(void *ud, void *ptr, size_t osize, size_t nsize) {
    alloc_state *as = (alloc_state *)ud;
    if (nsize == 0) {
        as->used -= osize;
        free(ptr);
        return NULL;
    }
    if (nsize > osize && as->used + (nsize - osize) > as->cap) {
        return NULL; /* refuse growth: PUC raises LUA_ERRMEM */
    }
    void *np = realloc(ptr, nsize);
    if (np != NULL) {
        as->used += nsize;
        as->used -= osize;
    }
    return np;
}

/* The registry is shared with coroutines, but not with other Exec calls. */
static void budget_hook(lua_State *L, lua_Debug *ar) {
    (void)ar;
    luaL_error(L, ORACLE_LIMIT_SENTINEL ": instruction budget");
}

static void limit_hook(lua_State *L, lua_Debug *ar) {
    (void)ar;
    limit_state *ls;
    lua_getfield(L, LUA_REGISTRYINDEX, "WANGSHU_ORACLE_LIMIT");
    ls = (limit_state *)lua_touserdata(L, -1);
    lua_pop(L, 1);
    if (ls == NULL) {
        luaL_error(L, ORACLE_LIMIT_SENTINEL ": limit state missing");
    }
    check_wall_time(ls);
    if (ls->limit_reason != NULL) {
        luaL_error(L, "%s", ls->limit_reason);
    }
    if (ls->remaining > 0) {
        ls->remaining -= lua_gethookcount(L);
        if (ls->remaining <= 0) {
            ls->limit_reason = ORACLE_LIMIT_SENTINEL ": instruction budget";
            luaL_error(L, "%s", ls->limit_reason);
        }
        if (ls->remaining < lua_gethookcount(L)) {
            lua_sethook(L, limit_hook, LUA_MASKCOUNT, ls->remaining);
            ls->hook_interval = ls->remaining;
        }
    }
}

/* run one chunk already on the stack (compiled function at -1);
 * returns 0 on success, else leaves error message on stack. */
static int run_top(lua_State *L) {
    return lua_pcall(L, 0, 0, 0);
}

/* portable substring search (memmem is a GNU extension). */
static int contains(const char *hay, size_t hay_len,
                    const char *needle, size_t needle_len) {
    if (needle_len == 0 || hay_len < needle_len) {
        return 0;
    }
    for (size_t i = 0; i + needle_len <= hay_len; i++) {
        if (memcmp(hay + i, needle, needle_len) == 0) {
            return 1;
        }
    }
    return 0;
}

int wangshu_oracle_exec(const char *src, size_t src_len,
                        const char *prelude, size_t prelude_len,
                        size_t max_alloc, int budget,
                        int64_t wall_time_ns,
                        char **out, size_t *out_len,
                        char **err, size_t *err_len) {
    *out = NULL;
    *out_len = 0;
    *err = NULL;
    *err_len = 0;

    alloc_state as;
    as.used = 0;
    as.cap = max_alloc;

    lua_State *L = lua_newstate(capped_alloc, &as);
    if (L == NULL) {
        return WANGSHU_ORACLE_LIMIT;
    }

    int verdict = WANGSHU_ORACLE_OK;
    limit_state ls;
    ls.started_ns = 0;
    ls.wall_time_ns = wall_time_ns;
    ls.remaining = budget;
    ls.hook_interval = budget > 0 && budget < 1000 ? budget : 1000;
    ls.limit_reason = NULL;

    luaL_openlibs(L);

    /* prelude: output capture + stdlib trim + determinism stubs.
     * A prelude failure is a harness bug, not a fuzz result: report
     * it as LIMIT (uncomparable) with the message for debugging. */
    if (luaL_loadbuffer(L, prelude, prelude_len, "=prelude") != 0 ||
        run_top(L) != 0) {
        verdict = WANGSHU_ORACLE_LIMIT;
        goto harvest_error;
    }

    /* stash the readout function in the registry NOW: the fuzz script
     * may overwrite the __oracle_readout global, but it cannot reach
     * the registry, so harvest below always calls the prelude's real
     * accumulator reader. */
    lua_getglobal(L, "__oracle_readout");
    lua_setfield(L, LUA_REGISTRYINDEX, "WANGSHU_ORACLE_OUT");

    /* install the budget AFTER the prelude: the prelude is trusted
     * harness code of trivial cost; the budget bounds fuzz input. */
    if (wall_time_ns > 0) {
        ls.started_ns = monotonic_ns();
        if (ls.started_ns < 0) {
            ls.limit_reason = ORACLE_LIMIT_SENTINEL ": monotonic clock unavailable";
        }
        lua_pushlightuserdata(L, &ls);
        lua_setfield(L, LUA_REGISTRYINDEX, "WANGSHU_ORACLE_LIMIT");
        lua_sethook(L, limit_hook, LUA_MASKCOUNT, budget > 0 && budget < 1000 ? budget : 1000);
    } else if (budget > 0) {
        lua_sethook(L, budget_hook, LUA_MASKCOUNT, budget);
    }

    /* chunkname "fuzz" matches the wangshu side (Compile(..., "fuzz"))
     * so location-prefixed error messages agree byte-for-byte. */
    if (luaL_loadbuffer(L, src, src_len, "fuzz") != 0) {
        verdict = WANGSHU_ORACLE_ERROR;
        goto harvest_error;
    }
    if (run_top(L) != 0) {
        verdict = WANGSHU_ORACLE_ERROR;
        goto harvest_error;
    }
    goto harvest_output;

harvest_error:
    lua_sethook(L, NULL, 0, 0); /* error path below runs harness code */
    {
        size_t n = 0;
        const char *msg = lua_tolstring(L, -1, &n);
        if (msg == NULL) { /* non-string error object (error({})) */
            msg = "(non-string error)";
            n = strlen(msg);
        }
        /* classify shim-imposed limits: LUA_ERRMEM ("not enough
         * memory") and the sentinel raised by budget_hook / the
         * prelude output cap. Position prefixes ([string "fuzz"]:N:)
         * precede the sentinel, so search, don't prefix-match. */
        if (verdict == WANGSHU_ORACLE_ERROR) {
            if (contains(msg, n, ORACLE_LIMIT_SENTINEL,
                         strlen(ORACLE_LIMIT_SENTINEL)) ||
                (n == strlen("not enough memory") &&
                 memcmp(msg, "not enough memory", n) == 0)) {
                verdict = WANGSHU_ORACLE_LIMIT;
            }
        }
        *err = (char *)malloc(n > 0 ? n : 1);
        if (*err != NULL) {
            memcpy(*err, msg, n);
            *err_len = n;
        }
        lua_pop(L, 1);
    }

harvest_output:
    lua_sethook(L, NULL, 0, 0);
    /* read back captured output: prelude stores the accumulator table
     * in the registry under WANGSHU_ORACLE_OUT_KEY; harvest applies
     * on both OK and ERROR verdicts (output-before-error compares). */
    lua_getfield(L, LUA_REGISTRYINDEX, "WANGSHU_ORACLE_OUT");
    if (lua_isfunction(L, -1)) {
        if (lua_pcall(L, 0, 1, 0) == 0 && lua_isstring(L, -1)) {
            size_t n = 0;
            const char *s = lua_tolstring(L, -1, &n);
            *out = (char *)malloc(n > 0 ? n : 1);
            if (*out != NULL) {
                memcpy(*out, s, n);
                *out_len = n;
            }
        }
        lua_pop(L, 1);
    } else {
        lua_pop(L, 1);
    }

    lua_close(L);
    return verdict;
}

void wangshu_oracle_free(char *p) { free(p); }
