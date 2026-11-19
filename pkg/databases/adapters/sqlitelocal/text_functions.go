//go:build cgo

package sqlitelocal

/*
#include "text_functions.h"
*/
import "C"

// registerTextFunctions registers the SQL functions editing a text value in place, text_patch and
// text_replace_between, on the connection.
func (c *Conn) registerTextFunctions() error {
	if rc := C.sqlitelocal_register_text_functions(c.db); rc != C.SQLITE_OK {
		return newError("register_text_functions", c.db, rc)
	}
	return nil
}
