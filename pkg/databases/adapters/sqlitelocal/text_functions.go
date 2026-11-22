//go:build cgo

package sqlitelocal

/*
#include "text_functions.h"
*/
import "C"

// registerTextFunctions registers the platform's SQL text functions on the connection: text_patch and
// text_replace_between, which edit a text value in place, and fts_all_words, which builds a full-text query.
func (c *Conn) registerTextFunctions() error {
	if rc := C.sqlitelocal_register_text_functions(c.db); rc != C.SQLITE_OK {
		return newError("register_text_functions", c.db, rc)
	}
	return nil
}
