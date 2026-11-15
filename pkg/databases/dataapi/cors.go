package dataapi

import "net/http"

// preflightMaxAge is how long, in seconds, a browser may reuse the answer to a preflight request before sending
// another: two hours, the longest Chrome honors.
const preflightMaxAge = "7200"

// allowAnyOrigin lets a page of any origin call the Data API from a browser. Every response allows any origin
// (Access-Control-Allow-Origin "*"), the GET and POST methods, and the Authorization and Content-Type request headers,
// but never credentials: a key in the Authorization header, not a cookie, authenticates a request. It also exposes the
// Retry-After response header, which a page could not read otherwise.
//
// A preflight request, an OPTIONS, is answered here with a 204 No Content, before any key is checked, and the browser
// may reuse that answer for preflightMaxAge.
func allowAnyOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		h.Set("Access-Control-Allow-Methods", "GET, POST")
		h.Set("Access-Control-Expose-Headers", "Retry-After")
		h.Set("Access-Control-Max-Age", preflightMaxAge)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OwnerUnreachable answers a Data API request that could not be forwarded to the pod owning its bdb, as that pod
// answers for a bdb that is briefly unavailable: a 503 with the unavailable code and a Retry-After header, allowing any
// origin so a page can read it. A preflight request is answered with a 204 No Content, as it needs nothing of the bdb.
func OwnerUnreachable(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", unavailableRetryAfter)
		respondError(w, http.StatusServiceUnavailable, codeUnavailable, "the bdb is briefly unavailable; retry shortly")
	})).ServeHTTP(w, r)
}
