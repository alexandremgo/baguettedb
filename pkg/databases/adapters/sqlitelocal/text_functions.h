/*
 * The platform's SQL text functions: text_patch and text_replace_between edit a text value in place, and
 * fts_all_words builds a full-text query from words typed as they come.
 */

#ifndef SQLITELOCAL_TEXT_FUNCTIONS_H
#define SQLITELOCAL_TEXT_FUNCTIONS_H

#include "sqlite3.h"

/*
 * Registers text_patch, text_replace_between and fts_all_words on db and returns an SQLite result code. All are
 * direct-only: a view, a trigger or a schema expression (CHECK, DEFAULT, index, generated column) calling one is
 * refused, so nothing stored in a database file depends on them.
 */
int sqlitelocal_register_text_functions(sqlite3 *db);

#endif
