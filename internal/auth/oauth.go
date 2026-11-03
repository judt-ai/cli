// Package auth signs the CLI in to Judt with OAuth.
//
// The CLI is a native public client whose client_id is a Client ID Metadata
// Document that Core serves at {issuer}/cli/client.json, so no install registers.
// It signs in with an authorization code and PKCE on a loopback redirect, or with
// the device authorization grant (RFC 8628) where no browser can reach it.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Scope is what the CLI asks for: read and change Apps, and a refresh token.
const Scope = "apps:read apps:write offline_access"

// Server is the authorization server, as its RFC 8414 metadata describes it.
type Server struct {
	Issuer                      string `json:"issuer"`
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	RevocationEndpoint          string `json:"revocation_endpoint"`
	// ClientID is the CLI's client document on this server.
	ClientID string `json:"-"`
	// Resource is the API the tokens are for.
	Resource string `json:"-"`
}

// Token is what a sign-in or refresh returns.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// Discover finds the API's authorization server: the API's RFC 9728 metadata names
// the issuer, and the issuer's RFC 8414 metadata names its endpoints.
func Discover(ctx context.Context, client *http.Client, apiURL string) (*Server, error) {
	var resource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := getJSON(ctx, client, strings.TrimRight(apiURL, "/")+"/.well-known/oauth-protected-resource", &resource); err != nil {
		return nil, fmt.Errorf("finding %s's sign-in server: %w", apiURL, err)
	}
	if len(resource.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("%s names no sign-in server", apiURL)
	}
	issuer := strings.TrimRight(resource.AuthorizationServers[0], "/")
	var s Server
	if err := getJSON(ctx, client, issuer+"/.well-known/oauth-authorization-server", &s); err != nil {
		return nil, fmt.Errorf("reading %s's sign-in metadata: %w", issuer, err)
	}
	if !strings.HasPrefix(issuer, "https://") {
		return nil, fmt.Errorf("%s isn't served over HTTPS, so it can't fetch the CLI's client document; sign in with an API token instead (judt auth login --with-token)", issuer)
	}
	s.ClientID = issuer + "/cli/client.json"
	s.Resource = resource.Resource
	if s.Resource == "" {
		s.Resource = strings.TrimRight(apiURL, "/")
	}
	return &s, nil
}

// Browser signs in with an authorization code and PKCE. It listens on a loopback
// port for the redirect, and calls open with the URL to show the user.
func Browser(ctx context.Context, client *http.Client, s *Server, open func(string)) (*Token, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening for the sign-in redirect: %w", err)
	}
	defer listener.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	verifier, state := random(32), random(16)
	challenge := sha256.Sum256([]byte(verifier))

	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {s.ClientID},
		"redirect_uri":          {redirect},
		"scope":                 {Scope},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"resource":              {s.Resource},
	}
	type result struct{ code, err string }
	done := make(chan result, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		res := result{code: q.Get("code")}
		switch {
		case q.Get("state") != state:
			res = result{err: "the sign-in answer doesn't match this request; run judt auth login again"}
		case q.Get("error") != "":
			res = result{err: strings.TrimSpace(q.Get("error") + ": " + q.Get("error_description"))}
		case res.code == "":
			res = result{err: "the sign-in answer has no code"}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		message := "You're signed in to the Judt CLI. You can close this tab."
		if res.err != "" {
			message = "Sign-in failed: " + res.err
		}
		fmt.Fprintf(w, "<!doctype html><title>Judt CLI</title><body style=\"font:16px system-ui;margin:4rem\"><p>%s</p>", html.EscapeString(message))
		select {
		case done <- res:
		default:
		}
	})}
	go server.Serve(listener)
	defer server.Close()

	open(s.AuthorizationEndpoint + "?" + query.Encode())
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil, errors.New("timed out waiting for the browser sign-in; run judt auth login again, or use --device")
	}
	if res.err != "" {
		return nil, errors.New(res.err)
	}
	return tokenRequest(ctx, client, s.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"client_id":     {s.ClientID},
		"code_verifier": {verifier},
		"resource":      {s.Resource},
	})
}

// DeviceCode is the code the user enters to approve a device sign-in.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartDevice asks for a device code.
func StartDevice(ctx context.Context, client *http.Client, s *Server) (*DeviceCode, error) {
	if s.DeviceAuthorizationEndpoint == "" {
		return nil, errors.New("this server doesn't offer device sign-in; run judt auth login without --device")
	}
	form := url.Values{"client_id": {s.ClientID}, "scope": {Scope}, "resource": {s.Resource}}
	body, status, err := postForm(ctx, client, s.DeviceAuthorizationEndpoint, form)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, oauthError(body, status)
	}
	var d DeviceCode
	if err := json.Unmarshal(body, &d); err != nil || d.DeviceCode == "" {
		return nil, fmt.Errorf("unexpected device sign-in answer: %s", truncate(body))
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	return &d, nil
}

// PollDevice waits until the user approves or denies the device code, or it expires.
func PollDevice(ctx context.Context, client *http.Client, s *Server, d *DeviceCode) (*Token, error) {
	interval := time.Duration(d.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if d.ExpiresIn > 0 && time.Now().After(deadline) {
			return nil, errors.New("the code expired; run judt auth login --device again")
		}
		t, err := tokenRequest(ctx, client, s.TokenEndpoint, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {d.DeviceCode},
			"client_id":   {s.ClientID},
		})
		var oe *Error
		switch {
		case err == nil:
			return t, nil
		case errors.As(err, &oe) && oe.Code == "authorization_pending":
		case errors.As(err, &oe) && oe.Code == "slow_down":
			interval += 5 * time.Second
		case errors.As(err, &oe) && oe.Code == "access_denied":
			return nil, errors.New("the sign-in was denied")
		case errors.As(err, &oe) && oe.Code == "expired_token":
			return nil, errors.New("the code expired; run judt auth login --device again")
		default:
			return nil, err
		}
	}
}

// Refresh exchanges a refresh token for a new access token. A server that doesn't
// rotate refresh tokens returns none, and the old one stays valid.
func Refresh(ctx context.Context, client *http.Client, tokenEndpoint, clientID, resource, refreshToken string) (*Token, error) {
	t, err := tokenRequest(ctx, client, tokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"resource":      {resource},
	})
	if err == nil && t.RefreshToken == "" {
		t.RefreshToken = refreshToken
	}
	return t, err
}

// Revoke revokes a refresh token (RFC 7009).
func Revoke(ctx context.Context, client *http.Client, endpoint, clientID, token string) error {
	if endpoint == "" {
		return nil
	}
	body, status, err := postForm(ctx, client, endpoint, url.Values{"token": {token}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return oauthError(body, status)
	}
	return nil
}

// Error is an OAuth error answer.
type Error struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *Error) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

func tokenRequest(ctx context.Context, client *http.Client, endpoint string, form url.Values) (*Token, error) {
	body, status, err := postForm(ctx, client, endpoint, form)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, oauthError(body, status)
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return nil, fmt.Errorf("unexpected token answer: %s", truncate(body))
	}
	expires := time.Now().Add(time.Hour)
	if t.ExpiresIn > 0 {
		expires = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return &Token{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresAt: expires}, nil
}

func oauthError(body []byte, status int) error {
	e := &Error{Status: status}
	if json.Unmarshal(body, e) != nil || e.Code == "" {
		return fmt.Errorf("sign-in server answered %d: %s", status, truncate(body))
	}
	return e
}

func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}

func getJSON(ctx context.Context, client *http.Client, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", u, resp.StatusCode)
	}
	return json.Unmarshal(body, v)
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
