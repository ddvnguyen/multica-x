package moct

import "testing"

// The prefixes are the wire contract with moct-be (exact-string matches).
func TestRefBuilders(t *testing.T) {
	const (
		id = "01972f7e-7e8d-77ef-a13d-1b10ce3e9c01"
	)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"workspace", WorkspaceRef(id), "multica:ws:" + id},
		{"expert", ExpertRef(id), "multica:agent:" + id},
		{"issue", IssueRef(id), "multica:issue:" + id},
		{"project", ProjectRef(id), "multica:project:" + id},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s ref = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}
