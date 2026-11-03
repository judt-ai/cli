// Package config keeps the CLI's sign-ins and the current App.
//
// A sign-in (Credential) is stored per profile in the OS keychain, or in
// credentials.json (mode 0600) in the config directory where there is no keychain.
// The current App is the nearest .judt.json from the working directory up.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

const keyringService = "judt"

// Credential is one profile's sign-in.
type Credential struct {
	// Type is "api_token" or "oauth".
	Type   string `json:"type"`
	APIURL string `json:"api_url"`
	// Token is an API token (Type api_token).
	Token string `json:"token,omitempty"`
	// OAuth (Type oauth).
	AccessToken        string    `json:"access_token,omitempty"`
	RefreshToken       string    `json:"refresh_token,omitempty"`
	ExpiresAt          time.Time `json:"expires_at,omitzero"`
	ClientID           string    `json:"client_id,omitempty"`
	TokenEndpoint      string    `json:"token_endpoint,omitempty"`
	RevocationEndpoint string    `json:"revocation_endpoint,omitempty"`
	Resource           string    `json:"resource,omitempty"`
}

// Dir is the config directory: $JUDT_CONFIG_DIR, else $XDG_CONFIG_HOME/judt, else
// ~/.config/judt (%AppData%\judt on Windows).
func Dir() (string, error) {
	if d := os.Getenv("JUDT_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "judt"), nil
	}
	if home, err := os.UserHomeDir(); err == nil && os.PathSeparator == '/' {
		return filepath.Join(home, ".config", "judt"), nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "judt"), nil
}

func useKeyring() bool { return os.Getenv("JUDT_NO_KEYRING") == "" }

// LoadCredential returns the profile's sign-in, or nil if there is none.
func LoadCredential(profile string) (*Credential, error) {
	if useKeyring() {
		if s, err := keyring.Get(keyringService, profile); err == nil {
			var c Credential
			if err := json.Unmarshal([]byte(s), &c); err != nil {
				return nil, fmt.Errorf("the saved sign-in for profile %q is damaged; run judt auth login", profile)
			}
			return &c, nil
		}
	}
	all, err := readFile()
	if err != nil {
		return nil, err
	}
	return all[profile], nil
}

// SaveCredential stores the profile's sign-in, in the keychain if there is one.
func SaveCredential(profile string, c *Credential) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if useKeyring() && keyring.Set(keyringService, profile, string(data)) == nil {
		return removeFromFile(profile)
	}
	all, err := readFile()
	if err != nil {
		return err
	}
	all[profile] = c
	return writeFile(all)
}

// DeleteCredential forgets the profile's sign-in.
func DeleteCredential(profile string) error {
	if useKeyring() {
		// Not found, or no keychain here: either way the file is what's left to clear.
		_ = keyring.Delete(keyringService, profile)
	}
	return removeFromFile(profile)
}

func credentialsPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "credentials.json"), nil
}

func readFile() (map[string]*Credential, error) {
	all := map[string]*Credential{}
	p, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", p, err)
	}
	return all, nil
}

func writeFile(all map[string]*Credential) error {
	p, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func removeFromFile(profile string) error {
	all, err := readFile()
	if err != nil || all[profile] == nil {
		return err
	}
	delete(all, profile)
	return writeFile(all)
}

// AppLinkFile is the file `judt apps use` writes.
const AppLinkFile = ".judt.json"

type appLink struct {
	AppID string `json:"app_id"`
}

// FindApp returns the App ID in the nearest .judt.json from dir up, and its path.
func FindApp(dir string) (string, string, error) {
	for {
		p := filepath.Join(dir, AppLinkFile)
		data, err := os.ReadFile(p)
		if err == nil {
			var link appLink
			if err := json.Unmarshal(data, &link); err != nil || link.AppID == "" {
				return "", p, fmt.Errorf("%s doesn't name an App; run judt apps use <app-id>", p)
			}
			return link.AppID, p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", nil
		}
		dir = parent
	}
}

// WriteApp saves the App ID in dir/.judt.json.
func WriteApp(dir, appID string) (string, error) {
	p := filepath.Join(dir, AppLinkFile)
	data, _ := json.MarshalIndent(appLink{AppID: appID}, "", "  ")
	return p, os.WriteFile(p, append(data, '\n'), 0o644)
}
