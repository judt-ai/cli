package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/judt-ai/cli/internal/api"
	"github.com/judt-ai/cli/internal/auth"
	"github.com/judt-ai/cli/internal/config"
)

func (s *state) profileName() string {
	if s.profile != "" {
		return s.profile
	}
	if p := os.Getenv("JUDT_PROFILE"); p != "" {
		return p
	}
	return "default"
}

// baseURL is --api-url, else JUDT_API_URL, else the saved sign-in's, else production.
func (s *state) baseURL(cred *config.Credential) string {
	switch {
	case s.apiURL != "":
		return strings.TrimRight(s.apiURL, "/")
	case os.Getenv("JUDT_API_URL") != "":
		return strings.TrimRight(os.Getenv("JUDT_API_URL"), "/")
	case cred != nil && cred.APIURL != "":
		return cred.APIURL
	}
	return api.DefaultURL
}

func (s *state) httpClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

// tokenClient calls the API with a fixed token.
func (s *state) tokenClient(baseURL, token string) *api.Client {
	c := api.NewClient(baseURL, s.version)
	c.Token = func(context.Context, bool) (string, error) { return token, nil }
	return c
}

// client calls the API as JUDT_API_KEY, else as the profile's sign-in. An OAuth
// access token is renewed when it has under a minute left, or after a 401.
func (s *state) client() (*api.Client, error) {
	if t := os.Getenv("JUDT_API_KEY"); t != "" {
		return s.tokenClient(s.baseURL(nil), t), nil
	}
	profile := s.profileName()
	cred, err := config.LoadCredential(profile)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, errNotSignedIn
	}
	if cred.Type == "api_token" {
		return s.tokenClient(s.baseURL(cred), cred.Token), nil
	}
	c := api.NewClient(s.baseURL(cred), s.version)
	c.CanRefresh = cred.RefreshToken != ""
	c.Token = func(ctx context.Context, refresh bool) (string, error) {
		if !refresh && time.Until(cred.ExpiresAt) > time.Minute {
			return cred.AccessToken, nil
		}
		if cred.RefreshToken == "" {
			return cred.AccessToken, nil
		}
		t, err := auth.Refresh(ctx, s.httpClient(), cred.TokenEndpoint, cred.ClientID, cred.Resource, cred.RefreshToken)
		if err != nil {
			return "", exitError{4, fmt.Sprintf("Your sign-in expired (%v). Run judt auth login.", err)}
		}
		cred.AccessToken, cred.RefreshToken, cred.ExpiresAt = t.AccessToken, t.RefreshToken, t.ExpiresAt
		if err := config.SaveCredential(profile, cred); err != nil {
			fmt.Fprintln(s.errOut, "Warning: couldn't save the renewed sign-in:", err)
		}
		return cred.AccessToken, nil
	}
	return c, nil
}

// currentApp is --app, else JUDT_APP, else the nearest .judt.json.
func (s *state) currentApp() (string, error) {
	if s.app != "" {
		return s.app, nil
	}
	if a := os.Getenv("JUDT_APP"); a != "" {
		return a, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	id, _, err := config.FindApp(dir)
	if err != nil {
		return "", usageError{err}
	}
	if id == "" {
		return "", usageError{fmt.Errorf("no App selected: pass --app <app-id>, set JUDT_APP, or run judt apps use <app-id> (judt apps list shows your Apps)")}
	}
	return id, nil
}
