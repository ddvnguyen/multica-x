package moct

// Wire types mirroring moct-be's team-memory JSON (schema v8). Fields match
// moct's src/surfaces/http/team-memory.ts responses as observed live in the
// M-98 check; moct-be nulls decode into pointer fields.

// Home is a record's home: {kind: task|channel|team|expert, ref: <ref>}.
type Home struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Actor is the writer identity moct-be takes on every note:
// {kind: human|member, id: <ref>}. Runs send kind "member" with the D2
// expert ref as id.
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Tag is a visibility tag: {kind, ref}. Records written against a task home
// carry the task tag, which is what scope filtering matches on.
type Tag struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Entry is one record in an inject block — prompt-ready, no bookkeeping.
type Entry struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Attrs    map[string]any `json:"attrs"`
	Home     Home           `json:"home"`
	Priority int            `json:"priority"`
	Pinned   bool           `json:"pinned"`
	Tokens   int            `json:"tokens"`
}

// Injection is GET /team/v1/memory/inject's response.
type Injection struct {
	Entries     []Entry      `json:"entries"`
	Omitted     int          `json:"omitted"`
	Duplicates  int          `json:"duplicates"`
	Conflicts   int          `json:"conflicts"`
	Tokens      int          `json:"tokens"`
	LayerTokens int          `json:"layerTokens"`
	Index       []IndexLayer `json:"index"`
}

// IndexLayer points at entries too large or too low-priority to inject,
// grouped by the layer they belong to.
type IndexLayer struct {
	Home  Home        `json:"home"`
	More  int         `json:"more"`
	Items []IndexItem `json:"items"`
}

// IndexItem is a deferred entry: available via memory_get by id.
type IndexItem struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Title         string   `json:"title"`
	Tokens        int      `json:"tokens"`
	Pinned        bool     `json:"pinned"`
	ConflictsWith []string `json:"conflictsWith,omitempty"`
}

// Record is a full memory record as memory_search / memory_get return it.
type Record struct {
	ID            string         `json:"id"`
	WorkspaceID   string         `json:"workspaceId"` // null (how_to_work) decodes to ""
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	Attrs         map[string]any `json:"attrs"`
	Home          Home           `json:"home"`
	Tags          []Tag          `json:"tags"`
	Priority      int            `json:"priority"`
	Pinned        bool           `json:"pinned"`
	Status        string         `json:"status"` // active|pending|...
	Flag          *string        `json:"flag"`
	Supersedes    *string        `json:"supersedes"`
	SupersededBy  *string        `json:"supersededBy"`
	ConflictsWith []string       `json:"conflictsWith"`
	TaskRef       *string        `json:"taskRef"`
	UniqueKey     *string        `json:"uniqueKey"`
	AuthorKind    string         `json:"authorKind"`
	AuthorID      string         `json:"authorId"`
	CreatedAt     string         `json:"createdAt"`
	UpdatedAt     string         `json:"updatedAt"`
	LastUsedAt    *string        `json:"lastUsedAt"`
	UseCount      int            `json:"useCount"`
	Version       int            `json:"version"`
	Tokens        int            `json:"tokens"`
}

// SearchResult is GET /team/v1/memory's response. NextCursor is "" at the
// end (moct-be sends null).
type SearchResult struct {
	Results    []Record `json:"results"`
	Total      int      `json:"total"`
	NextCursor string   `json:"nextCursor"`
}

// GetResult is POST /team/v1/memory/get's response.
type GetResult struct {
	Records []Record `json:"records"`
	Missing []string `json:"missing"`
}

// NearMatch is what moct-be's near-duplicate check found: the write was
// held back until the caller resends with ConfirmNew.
type NearMatch struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Title  string `json:"title"`
	Home   Home   `json:"home"`
	Status string `json:"status"`
}

// NoteResult is POST /team/v1/memory's response: 201 with Record written,
// or 200 with Record nil and NearMatches filled. Notice carries a
// human-readable caveat (e.g. "Saved as pending...").
type NoteResult struct {
	Record      *Record     `json:"record"`
	NearMatches []NearMatch `json:"nearMatches"`
	Notice      string      `json:"notice"`
}
