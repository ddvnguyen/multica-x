// Package freebuffrt drives a headless Freebuff orchestrator over its local
// HTTP/SSE API.
//
// The orchestrator is the same process the Freebuff Desktop app runs behind
// its window: it owns the agent loop, tools, session slot and account. It
// binds 127.0.0.1 on a free port, announces itself on stdout, and accepts
// calls that carry the launch id the caller chose. This package contains no
// process spawning — launching goes through the agent package so the daemon's
// process-lifecycle rules apply — only the bundle layout, the launch
// environment, the ready announcement and the API client.
//
// The orchestrator API is internal to Freebuff and undocumented; the routes
// used here were read from the exported Desktop bundle (see Client). Callers
// should treat any unexpected shape as "unsupported orchestrator version".
package freebuffrt
