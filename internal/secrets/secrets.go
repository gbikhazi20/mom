// Package secrets stores values that must never land in the ~/.mom repo
// (API keys, tokens). Config refers to them as ${secret:NAME}; mom resolves
// the references when it writes each agent's native config.
//
// The default store is the macOS login Keychain (service "mom"). Tests and
// other platforms can set MOM_SECRETS_FILE to use a JSON file instead.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var ErrNotFound = errors.New("secret not found")

type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
}

func Default() Store {
	if f := os.Getenv("MOM_SECRETS_FILE"); f != "" {
		return FileStore{f}
	}
	return Keychain{Service: "mom"}
}

// Keychain uses the `security` CLI, so no cgo and no extra permissions
// prompt: items it creates are readable by it without a dialog.
type Keychain struct{ Service string }

func (k Keychain) Get(name string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", k.Service, "-a", name, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (k Keychain) Set(name, value string) error {
	out, err := exec.Command("security", "add-generic-password", "-U", "-s", k.Service, "-a", name, "-w", value).CombinedOutput()
	if err != nil {
		return fmt.Errorf("keychain: %v: %s", err, out)
	}
	return nil
}

type FileStore struct{ Path string }

func (f FileStore) load() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(f.Path); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func (f FileStore) Get(name string) (string, error) {
	v, ok := f.load()[name]
	if !ok {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return v, nil
}

func (f FileStore) Set(name, value string) error {
	m := f.load()
	m[name] = value
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(f.Path, b, 0o600)
}

var refRE = regexp.MustCompile(`\$\{secret:([A-Za-z0-9_.-]+)\}`)

func Ref(name string) string { return "${secret:" + name + "}" }

// Refs lists secret names referenced in s.
func Refs(s string) []string {
	var out []string
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// Expand replaces every ${secret:NAME} in s using store.
func Expand(s string, store Store) (string, error) {
	var firstErr error
	out := refRE.ReplaceAllStringFunc(s, func(m string) string {
		name := refRE.FindStringSubmatch(m)[1]
		v, err := store.Get(name)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return v
	})
	return out, firstErr
}

var (
	secretKeyRE   = regexp.MustCompile(`(?i)(key|token|secret|passw|auth|bearer|credential|cookie)`)
	secretValueRE = regexp.MustCompile(`^(sk-|ctx7sk-|ghp_|gho_|github_pat_|xox[abpr]-|lin_api_|Bearer\s)`)
	envNameRE     = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	nonAlnumRE    = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// LooksSecret guesses whether a header or env entry holds a credential.
func LooksSecret(key, value string) bool {
	if value == "" || refRE.MatchString(value) || strings.Contains(value, "${") {
		return false
	}
	return secretKeyRE.MatchString(key) || secretValueRE.MatchString(value)
}

// NameFor picks a Keychain name for a secret found under key in server.
func NameFor(server, key string) string {
	if envNameRE.MatchString(key) {
		return key
	}
	return strings.Trim(strings.ToUpper(nonAlnumRE.ReplaceAllString(server+"_"+key, "_")), "_")
}
