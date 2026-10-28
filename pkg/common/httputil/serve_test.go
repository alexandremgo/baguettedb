package httputil

import (
	"net/http"
	"testing"
	"time"
)

// TestApplyServerDefaults pins the server timeouts: the header and idle timeouts are filled in when zero and kept
// when set, while the whole-request read and write timeouts stay unset so streaming responses survive.
func TestApplyServerDefaults(t *testing.T) {
	t.Run("fills the zero-valued header and idle timeouts only", func(t *testing.T) {
		srv := &http.Server{}
		applyServerDefaults(srv)
		if srv.ReadHeaderTimeout != defaultReadHeaderTimeout {
			t.Errorf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, defaultReadHeaderTimeout)
		}
		if srv.IdleTimeout != defaultIdleTimeout {
			t.Errorf("IdleTimeout = %v, want %v", srv.IdleTimeout, defaultIdleTimeout)
		}
		// Streaming responses depend on these staying unset.
		if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
			t.Errorf("ReadTimeout/WriteTimeout = %v/%v, want both unset", srv.ReadTimeout, srv.WriteTimeout)
		}
	})
	t.Run("keeps explicit values", func(t *testing.T) {
		srv := &http.Server{ReadHeaderTimeout: time.Second, IdleTimeout: time.Minute}
		applyServerDefaults(srv)
		if srv.ReadHeaderTimeout != time.Second || srv.IdleTimeout != time.Minute {
			t.Errorf("timeouts = %v/%v, want the explicit 1s/1m", srv.ReadHeaderTimeout, srv.IdleTimeout)
		}
	})
}
