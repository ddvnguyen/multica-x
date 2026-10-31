// Package dshsession reads task transcripts out of the DSH session store.
//
// A dsh-runtime task (M-102) owns its transcript in the dsh session store, not
// in the task_message table: the runtime bridge patches dsh's
// session-persistence-jsonl plugin with
//
//	root: MULTICA_DSH_SESSION_ROOT ?? dshHomePath('sessions')
//
// so a task run whose daemon leaves MULTICA_DSH_SESSION_ROOT unset writes its
// session into the shared store every dsh surface on the machine reads:
//
//	<store>/<projectKey(cwd)>/<sessionId>/<generation>.jsonl[.zstd]
//
// The on-disk names are produced by dsh's session-persistence-jsonl format
// layer (projectKey / encodeSegment / generationLogFilename). The functions
// here mirror those rules byte for byte; a drift breaks transcript reads, so
// each is pinned by a test against the shapes observed on a live store.
//
// Reading is intentionally read-only and tolerant: a session that is still
// being written, half-compacted, or written by a newer dsh than this code
// must come back as whatever prefix is readable, never as a failed request.
package dshsession

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ErrNoSession reports that the store holds no transcript for the requested
// session — the ordinary state for a task whose daemon still used the private
// MULTICA_DSH_SESSION_ROOT, or one that failed before dsh created a session.
// Callers render an empty transcript for it, not an error page.
var ErrNoSession = errors.New("dsh session not found in store")

// sessionIDPattern is what dsh's encodeSegment passes through unchanged:
// [A-Za-z0-9._-]. Any id outside it would need ~XXXX escaping on disk, so a
// raw id containing anything else (including a path separator) cannot name a
// directory in this store and is rejected before it ever reaches a path.
// This is the traversal guard: store roots and cwd are operator configuration,
// but the session id can travel through untrusted surfaces.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateSessionID reports whether id can name a session directory as-is.
func ValidateSessionID(id string) bool {
	return id != "" && sessionIDPattern.MatchString(id)
}

// ProjectKey mirrors dsh's projectKey(cwd): filesystem separators and drive
// separators collapse to one '-', every other code unit outside
// [A-Za-z0-9._-] becomes '~XXXX' (uppercase hex, 4 digits), leading '-' runs
// are stripped, the readable core is truncated to 251 code units, and the
// whole key is wrapped in '--…--'.
//
// Escapes follow dsh's UTF-16 code-unit view of the string, so a rune outside
// the BMP encodes as its surrogate pair — two ~XXXX escapes — exactly as dsh
// would write it. An empty cwd maps to dsh's projectDir(undefined) layout.
func ProjectKey(cwd string) string {
	if cwd == "" {
		return "_no-cwd"
	}
	var b strings.Builder
	separatorRun := false
	for _, r := range cwd {
		if r == '/' || r == '\\' || r == ':' {
			if !separatorRun {
				b.WriteByte('-')
			}
			separatorRun = true
			continue
		}
		// Same safe set as dsh: [A-Za-z0-9._-], everything else escaped.
		if r != '~' && (r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			b.WriteRune(r)
			separatorRun = false
			continue
		}
		writeUTF16Escapes(&b, r)
		separatorRun = false
	}
	slug := strings.TrimLeft(b.String(), "-")
	if slug == "" {
		slug = "root"
	}
	if len(slug) > 251 {
		slug = truncateCodeUnits(slug, 251)
	}
	return "--" + slug + "--"
}

// writeUTF16Escapes escapes r the way dsh's per-code-unit loop does: BMP
// code points as one ~XXXX, astral ones as their surrogate pair.
func writeUTF16Escapes(b *strings.Builder, r rune) {
	if r <= 0xFFFF {
		fmt.Fprintf(b, "~%04X", r)
		return
	}
	r -= 0x10000
	hi := 0xD800 + (r >> 10)
	lo := 0xDC00 + (r & 0x3FF)
	fmt.Fprintf(b, "~%04X~%04X", hi, lo)
}

// truncateCodeUnits cuts s to at most n code units. The key was built from
// runes, so cutting rune-wise keeps every escape intact.
func truncateCodeUnits(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ReadTranscript returns the decoded JSONL events of one session, newest
// generation first. cwd resolves the project directory when known (the task's
// work_dir); an empty cwd falls back to scanning every project directory for
// the session id, which stays bounded by the store's project count.
func ReadTranscript(storeRoot, cwd, sessionID string) ([]Event, error) {
	if !ValidateSessionID(sessionID) {
		return nil, fmt.Errorf("%w: invalid session id %q", ErrNoSession, sessionID)
	}
	if storeRoot == "" {
		return nil, fmt.Errorf("%w: no dsh session store configured", ErrNoSession)
	}

	var dir string
	if cwd != "" {
		dir = filepath.Join(storeRoot, ProjectKey(cwd), sessionID)
	} else {
		found, err := scanForSession(storeRoot, sessionID)
		if err != nil {
			return nil, err
		}
		if found == "" {
			return nil, ErrNoSession
		}
		dir = found
	}

	logPath, err := newestGeneration(dir)
	if err != nil {
		return nil, err
	}
	return readLog(logPath)
}

// scanForSession looks for <storeRoot>/<any project>/<sessionID> and returns
// the first match. Session ids are unique per store in practice; if one ever
// appears under two project keys the first readdir order wins, which is
// acceptable for a read-only viewer.
func scanForSession(storeRoot, sessionID string) (string, error) {
	projects, err := os.ReadDir(storeRoot)
	if err != nil {
		return "", fmt.Errorf("read dsh session store: %w", err)
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(storeRoot, p.Name(), sessionID)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir, nil
		}
	}
	return "", nil
}

// generationLogName matches dsh's canonical generation basenames:
// session.jsonl (generation 0) and session.vN.jsonl (N >= 1), each optionally
// zstd-compressed. Anything else (partials, tmp names, archives) is ignored.
var generationLogName = regexp.MustCompile(`^session(?:\.v([1-9][0-9]*))?\.jsonl(\.zstd)?$`)

// newestGeneration picks the log file dsh itself would replay: the highest
// generation, zstd-compressed preferred over plaintext. A missing or empty
// directory is ErrNoSession, not an error page.
func newestGeneration(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoSession
		}
		return "", fmt.Errorf("read dsh session directory: %w", err)
	}
	bestPath, bestVersion, bestCompressed := "", -1, false
	for _, e := range entries {
		m := generationLogName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		version := 0
		if m[1] != "" {
			version, _ = strconv.Atoi(m[1])
		}
		compressed := m[2] == ".zstd"
		if version > bestVersion || version == bestVersion && compressed && !bestCompressed {
			bestPath, bestVersion, bestCompressed = filepath.Join(dir, e.Name()), version, compressed
		}
	}
	if bestPath == "" {
		return "", ErrNoSession
	}
	return bestPath, nil
}

// readLog decodes one session log: the header line first, then the event
// lines. Unparseable lines are skipped — a live session can be observed
// mid-append, and a truncated tail must cost nothing but its own events.
func readLog(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoSession
		}
		return nil, fmt.Errorf("open dsh session log: %w", err)
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".zstd") {
		// Streaming decode: a session being written may end mid-frame, and
		// readLog keeps whatever decoded before the truncation.
		dec, err := zstd.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("open zstd reader: %w", err)
		}
		defer dec.Close()
		r = dec
	}

	var events []Event
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		// A live session observed mid-append ends in a truncated frame; the
		// decoded prefix is still the transcript so far. Only an error with
		// nothing decoded is a real failure.
		if len(events) == 0 {
			return nil, fmt.Errorf("read dsh session log: %w", err)
		}
		return events, nil
	}
	return events, nil
}
