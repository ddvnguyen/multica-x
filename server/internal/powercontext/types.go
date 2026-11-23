package powercontext

// Wire types mirroring the PowerContext HTTP contract, which is served by the
// process itself at /openapi.json (openapi/powercontext.yaml upstream). Only
// the endpoints the seam uses are modeled: POST /v1/context/prepare (the
// pre-run push) and POST /v1/memory/remember (the post-run write).
//
// Fields the seam does not read are deliberately absent rather than guessed:
// a decoder that ignores them costs nothing, while a struct field invented
// from documentation prose would be a wire contract nobody verified.

// Section is one Artifact family in a prepare request's assembly.
type Section struct {
	// Family is memory | experience | profile | topic-memory.
	Family string `json:"family"`
	// Limit is the family's own entry cap (1–8; experience is 1–2).
	Limit int `json:"limit,omitempty"`
}

// Assembly selects and orders the sections prepare returns, and which optional
// metadata is shown. Format is "markdown" for the text seam.
type Assembly struct {
	Format   string    `json:"format,omitempty"`
	Sections []Section `json:"sections"`
	Show     []string  `json:"show,omitempty"`
}

// PrepareRequest is POST /v1/context/prepare. ScopeID is required and must be
// an existing Scope the caller can read; MaxBytes is the UTF-8 budget for the
// complete returned text (512–32768, default 8000).
type PrepareRequest struct {
	ScopeID string `json:"scope_id"`
	// Query drives recall. Empty is legal — the server then assembles from the
	// scope's own state (Profile, recent Memory) rather than a search.
	Query    string `json:"query,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
	// Assembly is omitted for the legacy selection (Memory 6 + Experience 2)
	// and set explicitly when the seam wants a different shape.
	Assembly *Assembly `json:"assembly,omitempty"`
	// IncludeReceipt asks for the diagnostic receipt (selected revisions,
	// retrieval modes, omissions). The seam does not send it on the pre-run
	// path: it is an ephemeral diagnostic and costs response bytes.
	IncludeReceipt bool `json:"include_receipt,omitempty"`
}

// Prepared is the prepare response envelope. Status is "ready" or "empty";
// an empty result carries Content null and ContentBytes 0.
type Prepared struct {
	Schema       string  `json:"schema"`
	Status       string  `json:"status"`
	Content      *string `json:"content"`
	ContentBytes int     `json:"content_bytes"`
}

// IsEmpty reports whether the server had nothing to inject. A nil Content on
// a "ready" status is treated as empty too: there is nothing to prepend, and
// the caller's job is the same either way.
func (p *Prepared) IsEmpty() bool {
	return p == nil || p.Status == "empty" || p.Content == nil || *p.Content == ""
}

// Text returns the assembled context, or "" when there is none.
func (p *Prepared) Text() string {
	if p.IsEmpty() {
		return ""
	}
	return *p.Content
}

// RememberRequest is POST /v1/memory/remember: one already-curated memory
// entry. Kind is the entry's kind (decision, constraint, fact, ...) and Text
// is plain UTF-8, limited to 8192 bytes by the server.
type RememberRequest struct {
	ScopeID string `json:"scope_id"`
	Kind    string `json:"kind,omitempty"`
	Text    string `json:"text"`
}

// RememberedRecord is one independent memory the server created or matched.
type RememberedRecord struct {
	Content      string          `json:"content"`
	State        string          `json:"state"`
	StateVersion int             `json:"state_version"`
	Artifact     ArtifactPointer `json:"artifact"`
}

// ArtifactPointer identifies the exact immutable revision behind a record.
type ArtifactPointer struct {
	Family     string `json:"family"`
	ArtifactID string `json:"artifact_id"`
	Revision   int    `json:"revision"`
}

// RememberResult is POST /v1/memory/remember's response.
type RememberResult struct {
	Records []RememberedRecord `json:"records"`
}
