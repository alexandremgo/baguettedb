// Package apptypes holds the transfer shapes that cross an app boundary through an *App port: the values one
// app's service hands to another app's consumer, whether the adapter in between is in-process or HTTP.
//
// Admission test: a type belongs here only if it crosses an app boundary through an *App port. Request-side
// DTOs, single-app response shapes, and authoring schemas such as starter manifests stay in their own layer.
//
// The json tags are the contract: intraprocess adapters pass these structs by value, HTTP adapters decode
// internal-endpoint responses straight into them, and a providing app's api and mcp layers reuse them for
// responses that are shape-identical to the contract. A consuming app may answer with a shape unchanged too, when
// its response is exactly what the provider hands it. Evolve them additively — a new field with omitempty stays
// wire-compatible — and review any change here as a cross-app API change. Converting an app's domain types into
// these shapes is the providing app's job (its appconv package); consumers never convert. Like pkg/common/quota,
// this is contract material that belongs to the boundary rather than to either side of it.
//
// Files are named after the providing app: databases.go holds the shapes pkg/databases provides, and a new
// providing app gets its own file.
package apptypes
