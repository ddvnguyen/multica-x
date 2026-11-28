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
// Text is the entry body — PowerContext's field is `text`, not the `content`
// of /v1/artifacts/*; a decoder written against the wrong one silently reads
// every entry as empty, which turns the dedupe search into "never found".
type RememberedRecord struct {
	Artifact     ArtifactPointer `json:"artifact"`
	Kind         string          `json:"kind"`
	Text         string          `json:"text"`
	State        string          `json:"state"`
	StateVersion int             `json:"state_version"`
	MergedIntoID *string         `json:"merged_into_id"`
}

// ArtifactPointer identifies the exact immutable revision behind a record.
type ArtifactPointer struct {
	Family     string `json:"family"`
	ArtifactID string `json:"artifact_id"`
	Revision   int    `json:"revision"`
}

// RememberResult is POST /v1/memory/remember's response. Changed is false
// when the server matched an existing entry instead of writing a new one.
type RememberResult struct {
	Changed bool               `json:"changed"`
	Records []RememberedRecord `json:"records"`
}

// SearchHit is one ranked result from /v1/memory/search. The entry itself sits
// under `memory`; the outer object carries the ranking.
type SearchHit struct {
	Memory    RememberedRecord `json:"memory"`
	Score     float64          `json:"score"`
	MatchedBy []string         `json:"matched_by"`
}

// scopePage is GET /v1/scopes' envelope, used only to resolve a Multica ref
// to the server-generated Scope id the other routes take.
type scopePage struct {
	Items      []scopeDescriptor `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

type scopeDescriptor struct {
	ScopeID            string                   `json:"scope_id"`
	ExternalReferences []scopeExternalReference `json:"external_references"`
}

type scopeExternalReference struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
