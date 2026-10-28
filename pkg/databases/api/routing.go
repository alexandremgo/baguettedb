package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	commonhttputil "github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// forwardedFromHeader marks a request that one databases pod has already proxied to another. If a pod receives a
// request bearing this header that it still doesn't own, the shard metadata and the routing target disagree —
// forwarding again would loop, so the request is failed instead.
const forwardedFromHeader = "X-Baguettedb-Shard-Forwarded-From"

// ShardRouter forwards bdb-scoped requests to the pod that owns the bdb. Ownership is (fleet color, shard
// ordinal): the owning pod serves locally; any other pod transparently reverse-proxies to the owner over per-pod
// headless DNS, so callers never need to know the topology. Installed whenever the pod has a fleet/shard identity
// (a StatefulSet pod); a pod that owns every bdb it sees simply serves locally after one lookup.
type ShardRouter struct {
	color   int16
	ordinal int16
	peerURL func(color, ordinal int16) string // base URL of the pod owning (color, ordinal)
	dbSvc   *service.DatabaseService
	logger  zerolog.Logger
}

// NewShardRouter builds a ShardRouter for the pod identified by (color, ordinal). peerURL maps an owner's
// (color, ordinal) to that pod's base URL (in production, rendered from the peer-URL template).
func NewShardRouter(
	color, ordinal int16, peerURL func(color, ordinal int16) string,
	dbSvc *service.DatabaseService, logger zerolog.Logger,
) *ShardRouter {
	return &ShardRouter{color: color, ordinal: ordinal, peerURL: peerURL, dbSvc: dbSvc, logger: logger}
}

// ownedHere reports whether this pod is the owner identified by (color, ordinal).
func (sr *ShardRouter) ownedHere(color, ordinal int16) bool {
	return color == sr.color && ordinal == sr.ordinal
}

// routeTarget decides where a bdb-scoped request goes: serveLocal=true when this pod should handle it (it owns
// the bdb, or the bdb is new and this is an active-fleet pod), otherwise the (color, ordinal) of the peer to
// forward to. A new bdb (not found) is steered to the active fleet by steerNewBdb.
func (sr *ShardRouter) routeTarget(
	ctx context.Context, bdbID uuid.UUID,
) (color, ordinal int16, serveLocal bool, err error) {
	ownerColor, ownerShard, found, err := sr.dbSvc.OwnerForBdb(ctx, bdbID)
	if err != nil {
		// serveLocal=true is the fail-safe: the middleware degrades to local on err, and a caller that ever
		// ignored err would too, rather than forwarding to a wrong (zero) target.
		return 0, 0, true, err
	}
	if found {
		if sr.ownedHere(ownerColor, ownerShard) {
			return 0, 0, true, nil
		}
		return ownerColor, ownerShard, false, nil
	}
	return sr.steerNewBdb(ctx)
}

// steerNewBdb decides where a not-yet-created bdb is created. creator-owns places the lazily-created file on
// whichever pod handles the request, so during a blue/green deploy a new bdb must be created on the active fleet
// — otherwise it lands on the draining fleet and is stranded after cutover. Serve locally when this pod is already
// on the active fleet; otherwise forward creation to this pod's active-color peer at the same ordinal.
func (sr *ShardRouter) steerNewBdb(ctx context.Context) (color, ordinal int16, serveLocal bool, err error) {
	activeColor, err := sr.dbSvc.ActiveColor(ctx)
	if err != nil {
		return 0, 0, true, err
	}

	// Forwards creation to the active fleet with the same ordinal
	if activeColor != sr.color {
		return activeColor, sr.ordinal, false, nil
	}

	// Already on the active fleet
	return 0, 0, true, nil
}

// Middleware resolves the owning shard for the request's bdb and either passes it through (this pod owns it,
// or it doesn't exist yet) or reverse-proxies it to the owner. A lookup error is logged and the request served
// locally — degrading to "maybe wrong pod" beats hard-failing every request on a transient Postgres blip.
func (sr *ShardRouter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bdbID, err := uuid.Parse(chi.URLParam(r, "bdbID"))
		if err != nil {
			// Malformed id — let the handler produce its normal 400.
			next.ServeHTTP(w, r)
			return
		}

		color, ordinal, serveLocal, err := sr.routeTarget(r.Context(), bdbID)
		if err != nil {
			sr.logger.Warn().Err(err).Str("bdb_id", bdbID.String()).
				Msg("shard routing: routing decision failed; serving locally")
			next.ServeHTTP(w, r)
			return
		}
		if serveLocal {
			next.ServeHTTP(w, r)
			return
		}

		sr.forward(w, r, color, ordinal)
	})
}

// forward reverse-proxies r to the pod owning shard, streaming the response back. Guards against routing loops:
// a request that was already forwarded once and still isn't local means metadata and DNS disagree.
func (sr *ShardRouter) forward(w http.ResponseWriter, r *http.Request, color, shard int16) {
	if from := r.Header.Get(forwardedFromHeader); from != "" {
		sr.logger.Error().Str("forwarded_from", from).
			Int16("my_color", sr.color).Int16("my_ordinal", sr.ordinal).
			Int16("owner_color", color).Int16("owner_shard", shard).
			Msg("shard routing loop: already-forwarded request still not local")
		commonhttputil.RespondJSON(w, http.StatusBadGateway, errorResponse{Error: "shard routing loop"})
		return
	}

	target, err := url.Parse(sr.peerURL(color, shard))
	if err != nil {
		sr.logger.Error().Err(err).Int16("owner_color", color).Int16("owner_shard", shard).
			Msg("shard routing: bad peer URL")
		commonhttputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	r.Header.Set(forwardedFromHeader, strconv.Itoa(int(sr.ordinal)))
	httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
}

// MCPProxy wraps an MCP handler so a tool call for a bdb owned by another shard is reverse-proxied to the
// owner, mirroring Middleware for the REST surface. MCP carries no bdb in the URL — it rides in the JSON-RPC
// tools/call arguments — so the body is buffered and peeked for bdb_id. The endpoint must run stateless so a
// forwarded call needs no prior session on the owner; the Authorization bearer is preserved by the reverse proxy
// so the owner re-derives the user. Requests with no bdb to route on (initialize, tools/list, notifications,
// owner-local calls) pass straight through. maxBodyBytes bounds buffering: an over-large body is served locally
// rather than buffered wholesale.
func (sr *ShardRouter) MCPProxy(maxBodyBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only POSTs carry a JSON-RPC body to inspect; anything else is served locally.
		if r.Method != http.MethodPost || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		// Restores the body for whichever path runs next (local handler or reverse proxy).
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		if err != nil || int64(len(body)) > maxBodyBytes {
			// Can't safely buffer the full body to forward it — serve locally; the handler applies its limit.
			next.ServeHTTP(w, r)
			return
		}

		bdbID, err := mcputil.BdbIDFromToolCallBody(body)
		if err != nil {
			// ErrNotToolCall is the normal case (initialize, tools/list, notifications) — stay silent. Any
			// other error means a tool call we couldn't read a bdb from: serve locally, but leave a
			// breadcrumb since that may be a request that should have routed.
			if !errors.Is(err, mcputil.ErrNotToolCall) {
				sr.logger.Warn().Err(err).
					Msg("shard routing (mcp): couldn't read bdb from tool call; serving locally")
			}
			next.ServeHTTP(w, r)
			return
		}

		color, ordinal, serveLocal, err := sr.routeTarget(r.Context(), bdbID)
		if err != nil {
			sr.logger.Warn().Err(err).Str("bdb_id", bdbID.String()).
				Msg("shard routing (mcp): routing decision failed; serving locally")
			next.ServeHTTP(w, r)
			return
		}
		if serveLocal {
			next.ServeHTTP(w, r)
			return
		}

		sr.forward(w, r, color, ordinal)
	})
}

// InternalBodyProxy wraps a bdb-scoped internal endpoint (called service-to-service over the load-balanced
// databases Service) so a request for a bdb owned by another shard is reverse-proxied to the owner. The bdb
// rides in the JSON body as bdb_id, not the URL, so the body is buffered and peeked. Requests with no bdb
// (or owner-local ones) pass straight through. maxBodyBytes bounds buffering; an over-large body serves locally.
func (sr *ShardRouter) InternalBodyProxy(maxBodyBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		// Restores the body for whichever path runs next (local handler or reverse proxy).
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		if err != nil || int64(len(body)) > maxBodyBytes {
			next.ServeHTTP(w, r)
			return
		}

		bdbID, ok := bdbIDFromInternalBody(body)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		color, ordinal, serveLocal, err := sr.routeTarget(r.Context(), bdbID)
		if err != nil {
			sr.logger.Warn().Err(err).Str("bdb_id", bdbID.String()).
				Msg("shard routing (internal): routing decision failed; serving locally")
			next.ServeHTTP(w, r)
			return
		}
		if serveLocal {
			next.ServeHTTP(w, r)
			return
		}

		sr.forward(w, r, color, ordinal)
	})
}

// bdbIDFromInternalBody extracts bdb_id from a bdb-scoped internal request body so InternalBodyProxy can
// route to the bdb's owner. Returns ok=false for a body with no/invalid bdb_id (e.g. the pod-local snapshot
// endpoints), in which case the caller serves the request locally.
func bdbIDFromInternalBody(body []byte) (uuid.UUID, bool) {
	var req struct {
		BdbID string `json:"bdb_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(req.BdbID)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// OrdinalFromPodName parses the StatefulSet ordinal from a pod name — the integer suffix after the final '-'
// (e.g. "databases-2" → 2, "my-svc-10" → 10). Returns an error for a name with no '-' or a non-numeric / empty
// suffix. (A negative ordinal can't arise: the suffix is taken after the last '-', so it never carries a sign.)
func OrdinalFromPodName(podName string) (int, error) {
	idx := strings.LastIndex(podName, "-")
	if idx == -1 {
		return 0, fmt.Errorf("pod name %q has no ordinal suffix", podName)
	}
	ordinal, err := strconv.Atoi(podName[idx+1:])
	if err != nil {
		return 0, fmt.Errorf("pod name %q has non-numeric ordinal: %w", podName, err)
	}

	return ordinal, nil
}

// BuildPeerURL renders a peer pod's base URL from the configured template by substituting {color} (0 -> "blue",
// 1 -> "green") and {ordinal}. E.g. "http://db-{color}-{ordinal}.{color}.example.internal:8002" with (color 1,
// ordinal 0) yields "http://db-green-0.green.example.internal:8002". Tokens may appear more than once.
func BuildPeerURL(template string, color, ordinal int16) string {
	colorStr := "blue"
	if color == 1 {
		colorStr = "green"
	}

	url := strings.ReplaceAll(template, "{color}", colorStr)
	url = strings.ReplaceAll(url, "{ordinal}", strconv.Itoa(int(ordinal)))

	return url
}
