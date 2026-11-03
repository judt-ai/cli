// Package api sends requests to the Judt API.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultURL is the production API.
const DefaultURL = "https://api.judt.ai"

// Client sends authenticated requests.
type Client struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
	// Token returns the bearer token. With refresh set, it renews an OAuth access
	// token first; CanRefresh says whether that can help after a 401.
	Token      func(ctx context.Context, refresh bool) (string, error)
	CanRefresh bool
}

// Request is one API call.
type Request struct {
	Method, Path string
	Query        url.Values
	// Body is sent as JSON when it isn't nil.
	Body any
	// IdempotencyKey is sent when it isn't empty.
	IdempotencyKey string
}

// Response is a successful answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Error is an API error answer: {"error": {"code", "message", "detail", "request_id"}}.
type Error struct {
	Status         int
	Code           string          `json:"code"`
	Message        string          `json:"message"`
	Detail         json.RawMessage `json:"detail"`
	RequestID      string          `json:"request_id"`
	IdempotencyKey string          `json:"-"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// DetailText is the error's detail as text: a string as it is, anything else as JSON.
func (e *Error) DetailText() string {
	if len(e.Detail) == 0 || string(e.Detail) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(e.Detail, &s) == nil {
		return s
	}
	return string(e.Detail)
}

// NewClient returns a client with the API's longest time limit (600 seconds).
func NewClient(baseURL, version string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		HTTP:      &http.Client{Timeout: 610 * time.Second},
		UserAgent: "judt-cli/" + version,
	}
}

// Do sends the request. A 401 with a refreshable token renews it and sends the
// request once more, with the same Idempotency-Key.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	resp, err := c.send(ctx, r, false)
	if err == nil && resp.Status == http.StatusUnauthorized && c.CanRefresh {
		resp, err = c.send(ctx, r, true)
	}
	if err != nil {
		return nil, err
	}
	if resp.Status >= 400 {
		e := &Error{Status: resp.Status, IdempotencyKey: r.IdempotencyKey}
		var wrapped struct {
			Error *Error `json:"error"`
		}
		wrapped.Error = e
		if json.Unmarshal(resp.Body, &wrapped) != nil || e.Code == "" {
			e.Code, e.Message = "http_"+fmt.Sprint(resp.Status), strings.TrimSpace(string(resp.Body))
		}
		if k := resp.Header.Get("Idempotency-Key"); k != "" {
			e.IdempotencyKey = k
		}
		return nil, e
	}
	return resp, nil
}

func (c *Client) send(ctx context.Context, r Request, refresh bool) (*Response, error) {
	token, err := c.Token(ctx, refresh)
	if err != nil {
		return nil, err
	}
	u := c.BaseURL + r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	var body io.Reader
	if r.Body != nil {
		data, err := json.Marshal(r.Body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", r.IdempotencyKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// NewKey returns a random Idempotency-Key (a UUID v4).
func NewKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
