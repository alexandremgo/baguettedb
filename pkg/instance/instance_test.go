package instance

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestMountAll_SkipsInternalRoutes pins the instance contract: apps are wired in-process, so internal
// service-to-service routes must not be exposed on the single public listener.
func TestMountAll_SkipsInternalRoutes(t *testing.T) {
	handler := func(http.ResponseWriter, *http.Request) {}
	sub := chi.NewRouter()
	sub.Get("/api/things/v1/things", handler)
	sub.Post("/internal/api/things/v1/things", handler)

	parent := chi.NewRouter()
	mountAll(parent, sub, false)

	mounted := map[string]bool{}
	err := chi.Walk(parent, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walking parent routes: %v", err)
	}

	if !mounted["GET /api/things/v1/things"] {
		t.Fatal("public route must be mounted on the instance router")
	}
	if mounted["POST /internal/api/things/v1/things"] {
		t.Fatal("internal route must not be mounted on the instance router")
	}
}

// TestMountAll_SkipRootLeavesTheSiteRootToTheParent pins that a sub-router's GET / is mounted by default and left
// out when the parent reserves the site root for the embedded web frontend, while every other route still mounts.
func TestMountAll_SkipRootLeavesTheSiteRootToTheParent(t *testing.T) {
	handler := func(http.ResponseWriter, *http.Request) {}

	for _, skipRoot := range []bool{false, true} {
		sub := chi.NewRouter()
		sub.Get("/", handler)
		sub.Get("/favicon.ico", handler)

		parent := chi.NewRouter()
		mountAll(parent, sub, skipRoot)

		mounted := map[string]bool{}
		err := chi.Walk(parent, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			mounted[method+" "+route] = true
			return nil
		})
		if err != nil {
			t.Fatalf("walking parent routes: %v", err)
		}
		if mounted["GET /"] == skipRoot {
			t.Fatalf("skipRoot=%v: GET / mounted = %v", skipRoot, mounted["GET /"])
		}
		if !mounted["GET /favicon.ico"] {
			t.Fatalf("skipRoot=%v: other routes must still be mounted", skipRoot)
		}
	}
}
