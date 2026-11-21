/*
 * Implements the SQL functions editing a text value in place.
 *
 *   text_patch(text, old, new)                  replaces the single occurrence of old with new.
 *   text_replace_between(text, start, end, new) replaces the single block running from the first byte of start to
 *                                               the last byte of end with new.
 *
 * Both raise an error instead of guessing: an argument that is NULL, an empty match string, a match string that is
 * absent, or one found more than once. Matching is byte for byte, and overlapping occurrences count separately
 * ("aa" is found twice in "aaa"). Arguments of another type are read as text, as the built-in replace() reads them.
 */

#include "text_functions.h"

#include <stdarg.h>
#include <string.h>

/* A run of bytes inside an argument value: not NUL-terminated, valid for the duration of the call. */
typedef struct {
    const unsigned char *p;
    sqlite3_int64 n;
} span;

/* Sets the result to an error whose message is built from an sqlite3_mprintf format. */
static void fail(sqlite3_context *ctx, const char *format, ...) {
    va_list ap;
    char *msg;

    va_start(ap, format);
    msg = sqlite3_vmprintf(format, ap);
    va_end(ap);
    if (msg == NULL) {
        sqlite3_result_error_nomem(ctx);
        return;
    }
    sqlite3_result_error(ctx, msg, -1);
    sqlite3_free(msg);
}

/*
 * Reads the argument called name of the function fn into out. Returns 0 after setting an error when the value is
 * NULL, or empty while non_empty is set.
 */
static int read_arg(sqlite3_context *ctx, const char *fn, const char *name, sqlite3_value *value, int non_empty,
                    span *out) {
    if (sqlite3_value_type(value) == SQLITE_NULL) {
        fail(ctx, "%s: %s is NULL", fn, name);
        return 0;
    }
    /* The byte count is only meaningful once the value has been converted to text. */
    out->p = sqlite3_value_text(value);
    out->n = sqlite3_value_bytes(value);
    if (out->p == NULL) {
        sqlite3_result_error_nomem(ctx);
        return 0;
    }
    if (non_empty && out->n == 0) {
        fail(ctx, "%s: %s is empty", fn, name);
        return 0;
    }
    return 1;
}

/* Returns the offset of the first occurrence of the non-empty needle in hay at or after from, or -1. */
static sqlite3_int64 find_from(span hay, span needle, sqlite3_int64 from) {
    while (from + needle.n <= hay.n) {
        /*
         * Skips ahead to the next byte equal to the first byte of needle, the only places a match can start. The
         * scan stops where needle would no longer fit in what is left of hay, so a hit always has room for a full
         * comparison.
         */
        const unsigned char *hit = memchr(hay.p + from, needle.p[0], (size_t)(hay.n - needle.n - from + 1));
        if (hit == NULL) {
            return -1;
        }
        from = hit - hay.p;
        /* Compares the whole needle at that position: a matching first byte says nothing about the rest. */
        if (memcmp(hit, needle.p, (size_t)needle.n) == 0) {
            return from;
        }
        from++;
    }
    return -1;
}

/*
 * Counts the occurrences of the non-empty needle in hay at or after from, overlapping ones included, and stores
 * the offset of the first one in *first.
 */
static sqlite3_int64 count_from(span hay, span needle, sqlite3_int64 from, sqlite3_int64 *first) {
    sqlite3_int64 count = 0;
    sqlite3_int64 at;

    for (at = find_from(hay, needle, from); at >= 0; at = find_from(hay, needle, at + 1)) {
        if (count == 0) {
            *first = at;
        }
        count++;
    }
    return count;
}

/*
 * Returns 1 when count is exactly one. Otherwise sets an error naming the argument and where it was searched
 * (scope is "" or a suffix such as " after start"), and returns 0.
 */
static int require_single(sqlite3_context *ctx, const char *fn, const char *name, const char *scope,
                          sqlite3_int64 count) {
    if (count == 1) {
        return 1;
    }
    if (count == 0) {
        fail(ctx, "%s: %s not found%s: it must match the text exactly, whitespace and line breaks included", fn,
             name, scope);
        return 0;
    }
    fail(ctx, "%s: %s found %lld times%s: add surrounding text so that it matches exactly once", fn, name, count,
         scope);
    return 0;
}

/*
 * Sets the result to a copy of text in which the n bytes at offset at are replaced by repl, or to an error when
 * that copy would exceed the engine's length limit or cannot be allocated.
 *
 *   ctx   receives the result, or the error.
 *   text  is the original text, left untouched.
 *   at    is the offset, in bytes from the start of text, of the first byte to remove.
 *   n     is the number of bytes to remove.
 *   repl  holds the bytes put in their place; when empty, the n bytes are removed and nothing is inserted.
 */
static void splice(sqlite3_context *ctx, span text, sqlite3_int64 at, sqlite3_int64 n, span repl) {
    sqlite3_int64 len = text.n - n + repl.n;
    unsigned char *out;

    if (len > sqlite3_limit(sqlite3_context_db_handle(ctx), SQLITE_LIMIT_LENGTH, -1)) {
        sqlite3_result_error_toobig(ctx);
        return;
    }
    out = sqlite3_malloc64((sqlite3_uint64)len + 1);
    if (out == NULL) {
        sqlite3_result_error_nomem(ctx);
        return;
    }
    memcpy(out, text.p, (size_t)at);
    memcpy(out + at, repl.p, (size_t)repl.n);
    memcpy(out + at + repl.n, text.p + at + n, (size_t)(text.n - at - n));
    out[len] = 0;
    sqlite3_result_text64(ctx, (char *)out, (sqlite3_uint64)len, sqlite3_free, SQLITE_UTF8);
}

/*
 * Implements text_patch(text, old, new): sets the result to text with its single occurrence of old replaced by new,
 * or to an error when old is absent or found more than once.
 */
static void text_patch(sqlite3_context *ctx, int argc, sqlite3_value **argv) {
    static const char fn[] = "text_patch";
    span text, old, repl;
    sqlite3_int64 at = 0;

    (void)argc;
    if (!read_arg(ctx, fn, "text", argv[0], 0, &text) || !read_arg(ctx, fn, "old", argv[1], 1, &old) ||
        !read_arg(ctx, fn, "new", argv[2], 0, &repl)) {
        return;
    }
    if (!require_single(ctx, fn, "old", "", count_from(text, old, 0, &at))) {
        return;
    }
    splice(ctx, text, at, old.n, repl);
}

/*
 * Implements text_replace_between(text, start, end, new): sets the result to text with the block running from start
 * to end, both included, replaced by new, or to an error when start is not found exactly once in text or end exactly
 * once after it.
 */
static void text_replace_between(sqlite3_context *ctx, int argc, sqlite3_value **argv) {
    static const char fn[] = "text_replace_between";
    span text, start, end, repl;
    sqlite3_int64 start_at = 0;
    sqlite3_int64 end_at = 0;

    (void)argc;
    if (!read_arg(ctx, fn, "text", argv[0], 0, &text) || !read_arg(ctx, fn, "start", argv[1], 1, &start) ||
        !read_arg(ctx, fn, "end", argv[2], 1, &end) || !read_arg(ctx, fn, "new", argv[3], 0, &repl)) {
        return;
    }
    if (!require_single(ctx, fn, "start", "", count_from(text, start, 0, &start_at))) {
        return;
    }
    /* The end is searched past the start match, so the two never overlap. */
    if (!require_single(ctx, fn, "end", " after start", count_from(text, end, start_at + start.n, &end_at))) {
        return;
    }
    splice(ctx, text, start_at, end_at + end.n - start_at, repl);
}

int sqlitelocal_register_text_functions(sqlite3 *db) {
    const int flags = SQLITE_UTF8 | SQLITE_DETERMINISTIC | SQLITE_DIRECTONLY;
    int rc;

    rc = sqlite3_create_function_v2(db, "text_patch", 3, flags, NULL, text_patch, NULL, NULL, NULL);
    if (rc != SQLITE_OK) {
        return rc;
    }
    return sqlite3_create_function_v2(db, "text_replace_between", 4, flags, NULL, text_replace_between, NULL, NULL,
                                      NULL);
}
