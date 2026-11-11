package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// run executes judt with args against a fake API and returns stdout, stderr and
// the exit code.
func run(t *testing.T, api http.HandlerFunc, args ...string) (string, string, int) {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	t.Setenv("JUDT_API_URL", server.URL)
	t.Setenv("JUDT_API_KEY", "judt_test")
	t.Setenv("JUDT_NO_UPDATE_CHECK", "1")
	t.Setenv("JUDT_CONFIG_DIR", t.TempDir())
	var out, errOut bytes.Buffer
	stdin, _ := os.Open(os.DevNull)
	t.Cleanup(func() { stdin.Close() })
	s := &state{version: "test", out: &out, errOut: &errOut, in: stdin}
	root := s.newRoot()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	cmd, err := root.ExecuteC()
	code := 0
	if err != nil {
		code = s.report(cmd, err)
	}
	return out.String(), errOut.String(), code
}

func noAPI(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected request %s %s", r.Method, r.URL) }
}

func TestEveryLayerHasHelp(t *testing.T) {
	cases := map[string][]string{
		"":                 {"Commands:", "apps", "auth", "Exit codes"},
		"apps":             {"Manage Apps:", "Source:", "Production:", "deploy", "files", "commit", "--app"},
		"auth":             {"judt auth login --device", "--with-token", "whoami", "logout"},
		"apps files":       {"list", "read", "write", "delete", "judt apps commit"},
		"apps deploy":      {"Publishes the App", "--commit-id", "--idempotency-key", "API call: POST /api/apps/{id}/deployments", "Examples:"},
		"apps secrets set": {"never a flag", "API call: PUT /api/apps/{id}/secrets/{name}"},
	}
	for path, want := range cases {
		args := append(strings.Fields(path), "--help")
		out, _, code := run(t, noAPI(t), args...)
		if code != 0 {
			t.Errorf("judt %s --help exited %d", path, code)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("judt %s --help lacks %q:\n%s", path, w, out)
			}
		}
	}
}

func TestEveryOperationHasHelpText(t *testing.T) {
	for _, op := range operations {
		if op.Summary == "" || op.Description == "" {
			t.Errorf("%s has no summary or description", op.ID)
		}
		if len(op.Example) == 0 {
			t.Errorf("%s (%s) has no example in commands.yaml", op.ID, op.Command)
		}
		if op.Section == "" && parentPath(op.Command) == "apps" {
			t.Errorf("%s sits directly under apps but has no section", op.ID)
		}
	}
}

func TestRequestFromArgsAndFlags(t *testing.T) {
	var got struct {
		method, path, key string
		body              map[string]any
	}
	api := func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.key = r.Method, r.URL.EscapedPath(), r.Header.Get("Idempotency-Key")
		data, _ := io.ReadAll(r.Body)
		got.body = nil
		_ = json.Unmarshal(data, &got.body)
		w.Write([]byte(`{"id":"app_1","name":"Blog","production":{"visibility":"private"}}`))
	}

	_, _, code := run(t, api, "apps", "update", "app_1", "--name", "Blog", "--production-visibility", "private")
	if code != 0 || got.method != "PATCH" || got.path != "/api/apps/app_1" {
		t.Fatalf("update: %d %s %s", code, got.method, got.path)
	}
	if got.body["name"] != "Blog" || got.body["production"].(map[string]any)["visibility"] != "private" || got.key == "" {
		t.Fatalf("update body %v key %q", got.body, got.key)
	}

	t.Setenv("JUDT_APP", "app_1")
	_, _, code = run(t, api, "apps", "files", "write", "src/a b.ts", "--content", "x")
	if code != 0 || got.method != "PUT" || got.path != "/api/apps/app_1/files/src/a%20b.ts" || got.body["content"] != "x" {
		t.Fatalf("write: %d %s %s %v", code, got.method, got.path, got.body)
	}

	_, _, code = run(t, api, "apps", "commit", "First version", "--idempotency-key", "k1")
	if code != 0 || got.path != "/api/apps/app_1/commits" || got.body["message"] != "First version" || got.key != "k1" {
		t.Fatalf("commit: %d %s %v %q", code, got.path, got.body, got.key)
	}
}

func TestUsageErrors(t *testing.T) {
	t.Setenv("JUDT_APP", "app_1")
	for _, args := range [][]string{
		{"apps", "deploy", "extra"},
		{"apps", "logs", "--limit", "many"},
		{"apps", "logs", "--environment", "staging"},
		{"apps", "create"}, // --name is required
		{"apps", "nope"},
	} {
		_, errOut, code := run(t, noAPI(t), args...)
		if code != 2 {
			t.Errorf("judt %v exited %d, want 2: %s", args, code, errOut)
		}
	}
}

func TestAPIErrorsAndRetryHint(t *testing.T) {
	t.Setenv("JUDT_APP", "app_1")
	api := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		w.Write([]byte(`{"error":{"code":"request_timeout","message":"Still publishing","request_id":"req_1"}}`))
	}
	_, errOut, code := run(t, api, "apps", "deploy", "--idempotency-key", "k2")
	if code != 1 || !strings.Contains(errOut, "request_timeout: Still publishing") || !strings.Contains(errOut, "--idempotency-key k2") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestPipedOutputIsTheAPIsJSON(t *testing.T) {
	api := func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"id":"app_1"}],"has_more":false,"next_cursor":"c"}`))
	}
	out, _, code := run(t, api, "apps", "list")
	if code != 0 || strings.TrimSpace(out) != `{"results":[{"id":"app_1"}],"has_more":false,"next_cursor":"c"}` {
		t.Fatalf("exit %d: %q", code, out)
	}
}

func TestTerminalText(t *testing.T) {
	var out, errOut bytes.Buffer
	s := &state{out: &out, errOut: &errOut, stdoutTTY: true}
	list := []byte(`{"results":[{"id":"app_1","name":"Blog","slug":"blog","url":"https://blog.judt.dev","production":{"visibility":"public"}}],"has_more":true,"next_cursor":"c2"}`)
	if err := s.print(nil, list); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ID     NAME  SLUG  URL") || !strings.Contains(out.String(), "app_1  Blog  blog  https://blog.judt.dev") {
		t.Errorf("table:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "--cursor c2") {
		t.Errorf("no next-page hint: %s", errOut.String())
	}
	out.Reset()
	if err := s.print(nil, []byte(`{"id":"app_1","production":{"visibility":"public"},"domain":null}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "production.visibility  public") || !strings.Contains(out.String(), "domain                 -") {
		t.Errorf("key/value:\n%s", out.String())
	}
	out.Reset()
	run := &Operation{Print: "output", Exit: "exit_code"}
	err := s.print(run, []byte(`{"output":"FAIL 1 test\n","exit_code":3}`))
	if out.String() != "FAIL 1 test\n" || err == nil || err.(exitError).code != 3 {
		t.Errorf("run: %q %v", out.String(), err)
	}
}
