package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Freebuff accounts on disk.
//
// A Freebuff runtime holds any number of accounts. Each one is a directory of
// orchestrator state — the login lives there — under the runtime's directory:
//
//	<profiles>/<runtime id>/accounts/<account id>/   orchestrator state of one account
//	<profiles>/<runtime id>/default                  id of the account tasks run on
//
// The daemon creates and deletes account directories; tasks only read them.

// FreebuffEnvAccount selects the account a run uses, overriding the runtime's
// default. It is read like the other Freebuff settings: runtime env first,
// then the daemon's environment.
const FreebuffEnvAccount = "FREEBUFF_ACCOUNT"

var freebuffAccountIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidFreebuffAccountID reports whether id is safe to use as a directory name.
func ValidFreebuffAccountID(id string) bool {
	return freebuffAccountIDPattern.MatchString(id)
}

// NewFreebuffAccountID returns a fresh account id.
func NewFreebuffAccountID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "a" + hex.EncodeToString(b[:])
}

// ErrNoFreebuffAccount means the runtime has no account to run on.
var ErrNoFreebuffAccount = errors.New("no Freebuff account: add one from the runtime page and sign in")

// FreebuffRuntimeDir is the directory that holds everything for one runtime.
// Tasks and the daemon's account flow both go through here, so they always
// agree on where an account lives.
func FreebuffRuntimeDir(cfg Config) (string, error) { return FreebuffProfileDir(cfg) }

// FreebuffAccountsDir is the directory that holds the runtime's accounts.
func FreebuffAccountsDir(cfg Config) (string, error) {
	dir, err := FreebuffRuntimeDir(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "accounts"), nil
}

// FreebuffAccountDir is the orchestrator state directory of one account.
func FreebuffAccountDir(cfg Config, accountID string) (string, error) {
	if !ValidFreebuffAccountID(accountID) {
		return "", fmt.Errorf("invalid freebuff account id %q", accountID)
	}
	dir, err := FreebuffAccountsDir(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, accountID), nil
}

// ListFreebuffAccounts returns the ids of the accounts on disk, sorted.
func ListFreebuffAccounts(cfg Config) ([]string, error) {
	dir, err := FreebuffAccountsDir(cfg)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && ValidFreebuffAccountID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// EnsureFreebuffAccountDir creates the account's directory (owner-only).
func EnsureFreebuffAccountDir(cfg Config, accountID string) (string, error) {
	dir, err := FreebuffAccountDir(cfg, accountID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// RemoveFreebuffAccount deletes the account's directory, login included.
// Removing an account that does not exist is not an error.
func RemoveFreebuffAccount(cfg Config, accountID string) error {
	dir, err := FreebuffAccountDir(cfg, accountID)
	if err != nil {
		return err
	}
	EvictFreebuffOrchestrator(dir)
	return os.RemoveAll(dir)
}

// SetFreebuffDefaultAccount records the account tasks run on. An empty id
// clears it.
func SetFreebuffDefaultAccount(cfg Config, accountID string) error {
	dir, err := FreebuffRuntimeDir(cfg)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "default")
	if accountID == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if !ValidFreebuffAccountID(accountID) {
		return fmt.Errorf("invalid freebuff account id %q", accountID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(accountID+"\n"), 0o600)
}

// FreebuffAccountForRun picks the account a run uses: FREEBUFF_ACCOUNT, else
// the runtime's default, else the only account. Whatever is picked must exist
// on disk.
func FreebuffAccountForRun(cfg Config) (string, error) {
	id := freebuffSetting(cfg, FreebuffEnvAccount)
	if id == "" {
		dir, err := FreebuffRuntimeDir(cfg)
		if err != nil {
			return "", err
		}
		if raw, err := os.ReadFile(filepath.Join(dir, "default")); err == nil {
			id = strings.TrimSpace(string(raw))
		}
	}
	ids, err := ListFreebuffAccounts(cfg)
	if err != nil {
		return "", err
	}
	if id == "" {
		if len(ids) == 1 {
			return ids[0], nil
		}
		return "", ErrNoFreebuffAccount
	}
	for _, have := range ids {
		if have == id {
			return id, nil
		}
	}
	return "", fmt.Errorf("Freebuff account %q does not exist on this machine", id)
}
