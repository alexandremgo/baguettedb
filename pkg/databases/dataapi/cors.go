package dataapi

import "net/http"

// preflightMaxAge is how long, in seconds, a browser may reuse the answer to a preflight request before sending
// another: two hours, the longest Chrome honors.
const preflightMaxAge = "7200"

// allowAnyOrigin lets a page of any origin call the Data API from a browser. Every response allows any origin
// (Access-Control-Allow-Origin "*"), the GET and POST methods, and the Authorization and Content-Type request headers,
// but never credentials: a key in the Authorization header, not a cookie, authenticates a request.
//
// A preflight request, an OPTIONS, is answered here with a 204 No Content, before any key is checked, and the browser
// may reuse that answer for preflightMaxAge.
func allowAnyOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		h.Set("Access-Control-Allow-Methods", "GET, POST")
		h.Set("Access-Control-Max-Age", preflightMaxAge)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
