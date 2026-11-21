/*
 * SQL functions editing a text value in place: text_patch and text_replace_between.
 */

#ifndef SQLITELOCAL_TEXT_FUNCTIONS_H
#define SQLITELOCAL_TEXT_FUNCTIONS_H

#include "sqlite3.h"

/*
 * Registers text_patch and text_replace_between on db and returns an SQLite result code. Both are direct-only: a
 * view, a trigger or a schema expression (CHECK, DEFAULT, index, generated column) calling one is refused, so
 * nothing stored in a database file depends on them.
 */
int sqlitelocal_register_text_functions(sqlite3 *db);

#endif
