package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/dshsession"
)

// The summary clock keeps its timers in memory, so a daemon restart would let
// every idle-but-warm session go cold without a summary. Each armed session
// therefore leaves an arm record beside it; the daemon re-arms the records
// that are still warm when it starts.

// dshArmSidecarName is the arm record's file name inside the session's store
// directory. It exists only while a clock is pending: fire and Cancel remove it.
const dshArmSidecarName = "arm.json"

// dshTaskSessionsDirName is the per-profile directory holding the private
// <runtimeID>/<agentID> session roots used when MESSAGE_SINK is not "dsh".
const dshTaskSessionsDirName = "dsh-sessions"

// dshArmRecord is what a restarted daemon needs to rebuild a clock. The work
// dir is stored because the project key in the store path is not reversible.
type dshArmRecord struct {
	TaskID            string    `json:"task_id"`
	WorkDir           string    `json:"work_dir"`
	Model             string    `json:"model"`
	EndedAt           time.Time `json:"ended_at"`
	FirstPromptTokens int64     `json:"first_prompt_tokens"`
	LastPromptTokens  int64     `json:"last_prompt_tokens"`
	PriorSessionID    string    `json:"prior_session_id,omitempty"`
}

func dshSessionStoreDir(storeRoot, workDir, sessionID string) string {
	if strings.TrimSpace(storeRoot) == "" || !dshsession.ValidateSessionID(sessionID) {
		return ""
	}
	return filepath.Join(storeRoot, dshsession.ProjectKey(workDir), sessionID)
}

// dshArmRecordPath is "" when the session has no usable store directory, so
// callers treat "no path" as "nothing to persist or remove".
func dshArmRecordPath(storeRoot, workDir, sessionID string) string {
	dir := dshSessionStoreDir(storeRoot, workDir, sessionID)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, dshArmSidecarName)
}

func dshSummarySidecarExists(storeRoot, workDir, sessionID string) bool {
	dir := dshSessionStoreDir(storeRoot, workDir, sessionID)
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, dshSummarySidecarName))
	return err == nil
}

func writeDshArmRecord(path string, record dshArmRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create dsh arm record dir: %w", err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode dsh arm record: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write dsh arm record: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish dsh arm record: %w", err)
	}
	return nil
}

func removeDshArmRecord(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// rearmDshSummaries walks every session under roots (<root>/<projectKey>/
// <sessionID>/) and re-arms the clock of each session that has an arm record,
// no summary yet, and is still before its summarize point. Records of sessions
// that already have a summary or are past the summarize point are removed.
// newRequest supplies the request's Summarize/RecordDecision; returning false
// leaves the session alone. It returns how many clocks were re-armed.
func rearmDshSummaries(clock *dshSummaryClock, roots []string, newRequest func(rec dshArmRecord, sessionID, storeRoot string) (dshSummaryRequest, bool)) int {
	if clock == nil || !clock.cfg.Enabled {
		return 0
	}
	rearmed := 0
	for _, root := range roots {
		projects, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, project := range projects {
			if !project.IsDir() {
				continue
			}
			sessions, err := os.ReadDir(filepath.Join(root, project.Name()))
			if err != nil {
				continue
			}
			for _, session := range sessions {
				if !session.IsDir() {
					continue
				}
				sessionID := session.Name()
				armPath := filepath.Join(root, project.Name(), sessionID, dshArmSidecarName)
				rec, err := readDshArmRecord(armPath)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					clock.log.Warn("dsh summary arm record unreadable; removing it",
						"session_id", sessionID, "error", err)
					removeDshArmRecord(armPath)
					continue
				}
				if dshsession.ProjectKey(rec.WorkDir) != project.Name() || rec.EndedAt.IsZero() {
					clock.log.Warn("dsh summary arm record does not match its session directory; removing it",
						"session_id", sessionID)
					removeDshArmRecord(armPath)
					continue
				}
				if dshSummarySidecarExists(root, rec.WorkDir, sessionID) {
					removeDshArmRecord(armPath)
					continue
				}
				if clock.dueIn(rec.Model, rec.EndedAt) <= 0 {
					clock.log.Debug("dsh summary clock not re-armed: session is already cold",
						"session_id", sessionID, "ended_at", rec.EndedAt)
					removeDshArmRecord(armPath)
					continue
				}
				req, ok := newRequest(rec, sessionID, root)
				if !ok {
					continue
				}
				clock.Arm(req)
				rearmed++
			}
		}
	}
	return rearmed
}

func readDshArmRecord(path string) (dshArmRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return dshArmRecord{}, err
	}
	var rec dshArmRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return dshArmRecord{}, err
	}
	return rec, nil
}

// dshTaskSessionRoots lists the existing private session roots
// <profileDir>/dsh-sessions/<runtimeID>/<agentID>; a missing tree is empty.
func dshTaskSessionRoots(profileDir string) []string {
	matches, err := filepath.Glob(filepath.Join(profileDir, dshTaskSessionsDirName, "*", "*"))
	if err != nil {
		return nil
	}
	roots := make([]string, 0, len(matches))
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && info.IsDir() {
			roots = append(roots, match)
		}
	}
	return roots
}

// dshSummaryStoreRoots is every session store this daemon may have armed.
func (d *Daemon) dshSummaryStoreRoots() ([]string, error) {
	if d.cfg.MessageSink == "dsh" {
		if root := dshSharedSessionStoreRoot(); root != "" {
			return []string{root}, nil
		}
		return nil, nil
	}
	profileDir, err := cli.ProfileDir(d.cfg.Profile)
	if err != nil {
		return nil, err
	}
	return dshTaskSessionRoots(profileDir), nil
}

// rearmDshSummaryClocks re-arms, at daemon start, the summary clock of every
// dsh session that is still cache-warm and has no summary. Failures are
// logged, never fatal: a lost clock only costs the summary.
func (d *Daemon) rearmDshSummaryClocks() int {
	if d.dshSummary == nil || !d.cfg.DshSummary.Enabled {
		return 0
	}
	roots, err := d.dshSummaryStoreRoots()
	if err != nil {
		d.logger.Warn("dsh summary clocks not re-armed: cannot list session stores", "error", err)
		return 0
	}
	rearmed := rearmDshSummaries(d.dshSummary, roots, d.newDshRearmRequest)
	if rearmed > 0 {
		d.logger.Info("re-armed dsh summary clocks after start", "count", rearmed)
	}
	return rearmed
}

// newDshRearmRequest builds the request for one persisted arm record. The
// summarizer is a dsh backend built from the same runtime resolution a task
// uses; it carries no per-task state beyond the task id used as request id.
func (d *Daemon) newDshRearmRequest(rec dshArmRecord, sessionID, storeRoot string) (dshSummaryRequest, bool) {
	// Private roots are <profile>/dsh-sessions/<runtimeID>/<agentID>; the
	// shared dsh store carries no runtime identity.
	runtimeID := ""
	if d.cfg.MessageSink != "dsh" {
		runtimeID = filepath.Base(filepath.Dir(storeRoot))
	}
	backendCfg := agent.Config{
		Logger:        d.logger,
		TaskID:        rec.TaskID,
		RuntimeID:     runtimeID,
		DaemonVersion: d.cfg.CLIVersion,
		Env:           map[string]string{"DSH_TELEMETRY_DISABLED": "1"},
	}
	if d.cfg.MessageSink != "dsh" {
		backendCfg.Env["MULTICA_DSH_SESSION_ROOT"] = storeRoot
	}
	if spec, ok := d.customProfileLaunchForRuntime(runtimeID); ok {
		backendCfg.ExecutablePath = spec.path
		backendCfg.CLIVersion = spec.version
		backendCfg.LaunchPrefix = agent.FilterLaunchPrefix("dsh", spec.fixedArgs, d.logger)
	} else if entry, ok := d.agents()["dsh"]; ok {
		backendCfg.ExecutablePath = entry.Path
		backendCfg.CLIVersion = d.agentVersion("dsh")
		backendCfg.BuiltinRuntime = true
	} else {
		d.logger.Warn("dsh summary clock not re-armed: no dsh runtime on this daemon",
			"session_id", sessionID, "runtime_id", runtimeID)
		return dshSummaryRequest{}, false
	}
	backend, err := agent.ResolveBackend("dsh", backendCfg)
	if err != nil {
		d.logger.Warn("dsh summary clock not re-armed: cannot create dsh backend",
			"session_id", sessionID, "error", err)
		return dshSummaryRequest{}, false
	}
	summarizer, ok := backend.(dshSessionSummarizer)
	if !ok {
		return dshSummaryRequest{}, false
	}
	return dshSummaryRequest{
		TaskID:            rec.TaskID,
		SessionID:         sessionID,
		WorkDir:           rec.WorkDir,
		StoreRoot:         storeRoot,
		Model:             rec.Model,
		FirstPromptTokens: rec.FirstPromptTokens,
		LastPromptTokens:  rec.LastPromptTokens,
		PriorSessionID:    rec.PriorSessionID,
		EndedAt:           rec.EndedAt,
		Summarize:         summarizer.SummarizeSession,
		RecordDecision:    d.recordDshSummaryDecision(rec.TaskID),
	}, true
}
