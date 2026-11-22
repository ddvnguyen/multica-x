package moctmcp

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/moct"
)

// M-501 spike: can the MOCT seam enforce two expert scopes? An implementing
// Agent reads its Role's knowledge scope but writes only into its own. The
// fake below models moct-be's documented visibility rule (workspace AND any
// scope tag); the server under test is the real moctmcp.Server with a
// dual-scope env. It is intentionally not wired from the daemon yet — slice 3
// owns that wiring — which is why shared memory stays disabled in slice 1.
const (
	spikeWS       = "multica:ws:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	spikeAgent    = "multica:agent:bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	spikeRole     = "multica:agent:cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	spikeOtherWS  = "multica:ws:dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	spikeTask     = "multica:issue:eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	spikeRoleMem  = "role-memory"
	spikeAgentMem = "agent-memory"
	spikeForeign  = "foreign-memory"
	spikeGuessed  = "guessed-id"
)

type spikeMemory struct {
	id  string
	ws  string
	tag moct.Tag
}

func spikeStore() []spikeMemory {
	return []spikeMemory{
		{id: spikeRoleMem, ws: spikeWS, tag: moct.Tag{Kind: "expert", Ref: spikeRole}},
		{id: spikeAgentMem, ws: spikeWS, tag: moct.Tag{Kind: "expert", Ref: spikeAgent}},
		{id: spikeForeign, ws: spikeOtherWS, tag: moct.Tag{Kind: "expert", Ref: spikeRole}},
	}
}

// spikeVisible mirrors the seam's documented scope filter: same workspace and
// one matching tag. Other workspaces and other experts' records are hidden,
// including when an id is guessed rather than discovered via search.
func spikeVisible(record spikeMemory, workspace, expert string) bool {
	return record.ws == workspace && record.tag.Ref == expert
}

func spikeRecordJSON(m spikeMemory) string {
	record := moct.Record{
		ID: m.id, WorkspaceID: m.ws, Type: "convention", Title: m.id, Body: "body",
		Home: moct.Home{Kind: "expert", Ref: m.tag.Ref},
		Tags: []moct.Tag{m.tag}, Status: "active",
	}
	encoded, _ := json.Marshal(record)
	return string(encoded)
}

func TestSpikeMOCTDualScopeIsolation(t *testing.T) {
	noteCalls := 0
	var lastNote moct.NoteParams

	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/team/v1/memory":
			if r.Method == http.MethodGet {
				q := r.URL.Query()
				// Forged tool arguments never reach here: scope is env-pinned.
				if q.Get("workspaceId") != spikeWS || q.Get("expertId") != spikeRole {
					t.Errorf("read scope = %s/%s, want role scope", q.Get("workspaceId"), q.Get("expertId"))
				}
				visible := make([]string, 0)
				for _, record := range spikeStore() {
					if spikeVisible(record, q.Get("workspaceId"), q.Get("expertId")) {
						visible = append(visible, spikeRecordJSON(record))
					}
				}
				_, _ = io.WriteString(w, `{"results":[`+strings.Join(visible, ",")+`],"total":`+strconv.Itoa(len(visible))+`}`)
				return
			}
			noteCalls++
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &lastNote); err != nil {
				t.Fatalf("decode note: %v", err)
			}
			// moct-be pins actor/workspace from the server-sent body; a write
			// aimed at the Role's expert home is refused by our boundary.
			if lastNote.Actor.ID != spikeAgent || lastNote.WorkspaceID != spikeWS {
				t.Errorf("write identity = %s/%s, want agent workspace", lastNote.Actor.ID, lastNote.WorkspaceID)
			}
			if lastNote.Home.Kind == "expert" && lastNote.Home.Ref == spikeRole {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"ok":false,"code":"INVALID_INPUT","error":"cannot write to another expert scope"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"record":{"id":"new-note"}}`)
		case "/team/v1/memory/get":
			q := r.URL.Query()
			var body struct {
				IDs []string `json:"ids"`
			}
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decode get: %v", err)
			}
			records := make([]string, 0)
			missing := make([]string, 0)
			for _, id := range body.IDs {
				found := false
				for _, record := range spikeStore() {
					if record.id == id && spikeVisible(record, q.Get("workspaceId"), q.Get("expertId")) {
						records = append(records, spikeRecordJSON(record))
						found = true
					}
				}
				if !found {
					missing = append(missing, id)
				}
			}
			_, _ = io.WriteString(w, `{"records":[`+strings.Join(records, ",")+`],"missing":[`+jsonStrings(missing)+`]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// Dual-scope env: reads are widened to the Role, writes stay the Agent.
	scope := Scope{
		WorkspaceRef:  spikeWS,
		ExpertRef:     spikeAgent,
		ReadExpertRef: spikeRole,
		TaskRef:       spikeTask,
	}

	// 1. Reads use the Role expert even when the tool asks for another scope.
	frames := drive(t, client, scope,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{"q":"lane","workspaceId":"`+spikeOtherWS+`","expertId":"`+spikeAgent+`"}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("search frames: %+v", frames)
	}
	if body := toolText(t, frames[0]); !strings.Contains(body, spikeRoleMem) || strings.Contains(body, spikeAgentMem) {
		t.Fatalf("search leaked out of Role scope: %s", body)
	}

	// 2. Guessed IDs and cross-workspace records are reported as missing, not
	//    disclosed. This is moct-be's authoritative filter over env-pinned refs.
	frames = drive(t, client, scope,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_get","arguments":{"ids":["`+spikeRoleMem+`","`+spikeAgentMem+`","`+spikeForeign+`","`+spikeGuessed+`"]}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("get frames: %+v", frames)
	}
	body := toolText(t, frames[0])
	if !strings.Contains(body, spikeRoleMem) {
		t.Fatalf("Role-scoped get failed: %s", body)
	}
	for _, hidden := range []string{spikeAgentMem, spikeForeign, spikeGuessed} {
		if !strings.Contains(body, hidden) {
			t.Fatalf("hidden id %q not reported as missing: %s", hidden, body)
		}
	}

	// 3. A write aimed at the Role scope is refused before moct-be sees it.
	before := noteCalls
	frames = drive(t, client, scope,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"T","body":"B","home":{"kind":"expert","ref":"`+spikeRole+`"}}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("note frames: %+v", frames)
	}
	if !strings.Contains(toolText(t, frames[0]), "own expert scope") {
		t.Fatalf("note to Role scope was not refused: %s", toolText(t, frames[0]))
	}
	if noteCalls != before {
		t.Fatalf("refused note reached moct-be: %d calls", noteCalls)
	}

	// 4. An ordinary write stays attributed to the Agent and lands in its own
	//    task home.
	frames = drive(t, client, scope,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"T","body":"B"}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("note frames: %+v", frames)
	}
	if noteCalls != before+1 {
		t.Fatalf("ordinary note did not reach moct-be: %d calls", noteCalls)
	}
	if lastNote.Actor.ID != spikeAgent || lastNote.Home.Kind != "task" || lastNote.Home.Ref != spikeTask {
		t.Fatalf("write target = %s / %+v, want Agent actor and task home", lastNote.Actor.ID, lastNote.Home)
	}
}

func jsonStrings(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		encoded, _ := json.Marshal(value)
		parts = append(parts, string(encoded))
	}
	return strings.Join(parts, ",")
}
