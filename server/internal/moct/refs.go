package moct

// The D2 ref convention (docs/moct-seam.md): every cross-reference between
// Multica and MOCT is a prefixed string, so each side can tell whose id it
// holds without a lookup. The prefixes are the contract — moct-be matches
// them as exact strings (live-verified, M-98).
const (
	WorkspaceRefPrefix = "multica:ws:"
	ExpertRefPrefix    = "multica:agent:"
	IssueRefPrefix     = "multica:issue:"
	ProjectRefPrefix   = "multica:project:"
)

// WorkspaceRef builds the workspace ref: multica:ws:<uuid>.
func WorkspaceRef(id string) string { return WorkspaceRefPrefix + id }

// ExpertRef builds the knowledge-profile ref: multica:agent:<uuid>.
// Multica owns execution identity; MOCT keys the knowledge profile on this.
func ExpertRef(id string) string { return ExpertRefPrefix + id }

// IssueRef builds the task ref for an issue: multica:issue:<uuid>. It goes
// in task Home refs, the tasks= scope param and inject's taskRef — never in
// a note's taskRef field (moct-be only accepts task:… there).
func IssueRef(id string) string { return IssueRefPrefix + id }

// ProjectRef builds the channel ref: multica:project:<uuid>.
func ProjectRef(id string) string { return ProjectRefPrefix + id }
