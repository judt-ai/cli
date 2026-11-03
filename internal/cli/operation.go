package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/judt-ai/cli/internal/api"
	"github.com/judt-ai/cli/internal/config"
)

// flagValue is a text flag whose help shows its kind (int, json, a list of
// choices) and whose value is checked when the command runs.
type flagValue struct {
	value, kind string
}

func (v *flagValue) String() string     { return v.value }
func (v *flagValue) Set(s string) error { v.value = s; return nil }
func (v *flagValue) Type() string       { return v.kind }

type boundParam struct {
	Param
	flag  string
	text  *flagValue
	isSet *bool
}

func flagName(p Param) string {
	name := p.Name
	if p.Parent != "" {
		name = p.Parent + "_" + p.Name
	}
	return strings.ReplaceAll(name, "_", "-")
}

func kindOf(p Param) string {
	switch p.Type {
	case "integer":
		return "int"
	case "number":
		return "number"
	case "array":
		return "list"
	case "json":
		return "json"
	}
	if joined := strings.Join(p.Enum, "|"); joined != "" && len(joined) <= 40 {
		return joined
	}
	return "string"
}

// flagHelp is the spec's description on one line, without Markdown backticks
// (pflag reads a backticked word as the value's name), plus what it accepts.
func flagHelp(p Param) string {
	text := strings.Join(strings.Fields(strings.ReplaceAll(p.Description, "`", "")), " ")
	if len(p.Enum) > 0 && len(strings.Join(p.Enum, "|")) > 40 {
		text += " One of: " + strings.Join(p.Enum, ", ") + "."
	}
	switch p.Type {
	case "array":
		text += " Separate values with commas."
	case "json":
		text += " Give it as JSON."
	}
	if p.Required {
		text += " Required."
	}
	return strings.TrimSpace(text)
}

func (s *state) operationCommand(op *Operation, name string) *cobra.Command {
	use := []string{name}
	if op.AppArg {
		use = append(use, "[app-id]")
	}
	for _, a := range op.Args {
		use = append(use, "<"+strings.ReplaceAll(a, "_", "-")+">")
	}
	if len(op.Query)+len(op.Body) > len(op.Args) || op.Keyed {
		use = append(use, "[flags]")
	}
	min, max := len(op.Args), len(op.Args)
	if op.AppArg {
		max++
	}
	cmd := &cobra.Command{
		Use:         strings.Join(use, " "),
		Aliases:     op.Aliases,
		Short:       op.Summary,
		Long:        leafHelp(op),
		Example:     examples(op.Example),
		GroupID:     op.Section,
		Annotations: map[string]string{"api": op.Method + " " + op.Path},
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < min || len(args) > max {
				want := fmt.Sprint(min)
				if max > min {
					want = fmt.Sprintf("%d or %d", min, max)
				}
				return usageError{fmt.Errorf("%s takes %s argument(s), got %d", cmd.CommandPath(), want, len(args))}
			}
			return nil
		},
	}

	positional := map[string]bool{}
	for _, a := range op.Args {
		positional[a] = true
	}
	secret := map[string]bool{}
	for _, f := range op.Secret {
		secret[f] = true
	}
	var query, body []boundParam
	bind := func(p Param) boundParam {
		b := boundParam{Param: p, flag: flagName(p)}
		if p.Type == "boolean" {
			b.isSet = cmd.Flags().Bool(b.flag, false, flagHelp(p))
		} else {
			b.text = &flagValue{kind: kindOf(p)}
			cmd.Flags().Var(b.text, b.flag, flagHelp(p))
		}
		return b
	}
	for _, p := range op.Query {
		query = append(query, bind(p))
	}
	for _, p := range op.Body {
		if p.Parent == "" && (positional[p.Name] || secret[p.Name]) {
			body = append(body, boundParam{Param: p})
			continue
		}
		body = append(body, bind(p))
	}
	var key string
	var yes, all bool
	if op.Keyed {
		cmd.Flags().StringVar(&key, "idempotency-key", "", "Retry an interrupted run: pass the key that the failed run printed, to pick up its work instead of repeating it.")
	}
	if op.Confirm {
		cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Don't ask for confirmation. Needed outside a terminal.")
	}
	paged := false
	for _, p := range op.Query {
		paged = paged || p.Name == "cursor"
	}
	if paged {
		cmd.Flags().BoolVar(&all, "all", false, "Fetch every page, not just the first.")
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return s.run(cmd, op, args, query, body, key, yes, all)
	}
	return cmd
}

// leafHelp is the API's description, then what the CLI adds: where the App and
// any Secret come from, and what a terminal shows.
func leafHelp(op *Operation) string {
	text := op.Description
	if text == "" {
		text = op.Summary + "."
	}
	var notes []string
	if op.AppScoped {
		n := "Acts on the App that --app names, else JUDT_APP, else the nearest .judt.json."
		if op.AppArg {
			n = "Acts on the App you name, else the one that --app names, else JUDT_APP, else the nearest .judt.json."
		}
		notes = append(notes, n)
	}
	for _, f := range op.Secret {
		notes = append(notes, fmt.Sprintf("The %s is read from stdin when you pipe it, or asked for without showing it. It's never a flag, so it stays out of your shell history.", f))
	}
	if op.Print != "" {
		notes = append(notes, fmt.Sprintf("In a terminal, prints only the result's `%s`. With --json, prints the whole result.", op.Print))
	}
	if op.Exit != "" {
		notes = append(notes, fmt.Sprintf("Exits with the result's `%s`.", op.Exit))
	}
	if op.ID == "create_app" {
		notes = append(notes, "Saves the new App in .judt.json in the current directory, unless that file exists, so the next commands act on it.")
	}
	if len(notes) > 0 {
		text += "\n\n" + strings.Join(notes, "\n")
	}
	return wrap(text, 80)
}

func (s *state) run(cmd *cobra.Command, op *Operation, args []string, query, body []boundParam, key string, yes, all bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// The App: a leading argument where the command takes one, else the current App.
	var appID string
	if op.AppScoped {
		if op.AppArg && len(args) > len(op.Args) {
			appID, args = args[0], args[1:]
		} else {
			id, err := s.currentApp()
			if err != nil {
				return err
			}
			appID = id
		}
	}
	values := map[string]string{}
	for i, a := range op.Args {
		values[a] = args[i]
	}

	path := op.Path
	if op.AppScoped {
		path = strings.Replace(path, "{id}", url.PathEscape(appID), 1)
	}
	for name, v := range values {
		placeholder := "{" + name + "}"
		if !strings.Contains(path, placeholder) {
			continue
		}
		escaped := url.PathEscape(v)
		if name == "path" {
			parts := strings.Split(strings.TrimPrefix(v, "/"), "/")
			for i, part := range parts {
				parts[i] = url.PathEscape(part)
			}
			escaped = strings.Join(parts, "/")
		}
		path = strings.Replace(path, placeholder, escaped, 1)
	}

	q := url.Values{}
	for _, b := range query {
		raw, set, err := s.flagText(cmd, b)
		if err != nil {
			return err
		}
		if set {
			if _, err := convert(b.Param, raw); err != nil {
				return err
			}
			q.Set(b.Name, raw)
		} else if b.Required {
			return usageError{fmt.Errorf("--%s is required", b.flag)}
		}
	}

	var payload map[string]any
	if op.HasBody {
		payload = map[string]any{}
		for _, b := range body {
			var value any
			switch {
			case values[b.Name] != "" && b.Parent == "":
				v, err := convert(b.Param, values[b.Name])
				if err != nil {
					return err
				}
				value = v
			case b.flag == "" && contains(op.Secret, b.Name):
				v, err := s.readSecret(b.Name, args)
				if err != nil {
					return err
				}
				value = v
			case b.flag != "":
				raw, set, err := s.flagText(cmd, b)
				if err != nil {
					return err
				}
				if set {
					v, err := convert(b.Param, raw)
					if err != nil {
						return err
					}
					value = v
				}
			}
			// A token for "this App" defaults to the current one.
			if value == nil && b.Name == "app_id" && b.Parent == "" {
				if id, err := s.currentApp(); err == nil {
					value = id
				}
			}
			if value == nil {
				if b.Required {
					return usageError{fmt.Errorf("--%s is required", flagName(b.Param))}
				}
				continue
			}
			if b.Parent == "" {
				payload[b.Name] = value
				continue
			}
			parent, _ := payload[b.Parent].(map[string]any)
			if parent == nil {
				parent = map[string]any{}
				payload[b.Parent] = parent
			}
			parent[b.Name] = value
		}
	}

	if op.Confirm && !yes {
		target := appID
		if target == "" {
			target = "this"
		}
		if !s.stdinTTY {
			return usageError{fmt.Errorf("%s needs --yes when it isn't run in a terminal", cmd.CommandPath())}
		}
		fmt.Fprintf(s.errOut, "%s %s? [y/N] ", op.Summary, target)
		answer, _ := bufio.NewReader(s.in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return exitError{1, "Canceled."}
		}
	}

	client, err := s.client()
	if err != nil {
		return err
	}
	if op.Keyed && key == "" {
		key = api.NewKey()
	}
	req := api.Request{Method: op.Method, Path: path, Query: q, IdempotencyKey: key}
	if payload != nil {
		req.Body = payload
	}
	resp, err := client.Do(ctx, req)
	if err != nil {
		return err
	}
	result := resp.Body
	if all {
		if result, err = s.allPages(ctx, client, req, result); err != nil {
			return err
		}
	}
	if op.ID == "create_app" {
		s.saveCreatedApp(result)
	}
	return s.print(op, result)
}

// allPages follows next_cursor until has_more is false and returns one list.
func (s *state) allPages(ctx context.Context, client *api.Client, req api.Request, first []byte) ([]byte, error) {
	var results []json.RawMessage
	page := first
	for {
		var p struct {
			Results    []json.RawMessage `json:"results"`
			HasMore    bool              `json:"has_more"`
			NextCursor string            `json:"next_cursor"`
		}
		if err := json.Unmarshal(page, &p); err != nil {
			return nil, err
		}
		results = append(results, p.Results...)
		if !p.HasMore || p.NextCursor == "" {
			return json.Marshal(map[string]any{"results": results, "has_more": false, "next_cursor": p.NextCursor})
		}
		q := url.Values{}
		for k, v := range req.Query {
			q[k] = v
		}
		q.Set("cursor", p.NextCursor)
		req.Query = q
		resp, err := client.Do(ctx, req)
		if err != nil {
			return nil, err
		}
		page = resp.Body
	}
}

func (s *state) saveCreatedApp(result []byte) {
	var app struct {
		ID string `json:"id"`
	}
	dir, err := os.Getwd()
	if json.Unmarshal(result, &app) != nil || app.ID == "" || err != nil {
		return
	}
	if _, err := os.Stat(dir + string(os.PathSeparator) + config.AppLinkFile); err == nil {
		fmt.Fprintf(s.errOut, "Created %s. %s already names another App; run judt apps use %s to switch.\n", app.ID, config.AppLinkFile, app.ID)
		return
	}
	if p, err := config.WriteApp(dir, app.ID); err == nil {
		fmt.Fprintf(s.errOut, "Saved %s as the current App in %s.\n", app.ID, p)
	}
}

// flagText is a flag's raw text, with @path and - read.
func (s *state) flagText(cmd *cobra.Command, b boundParam) (string, bool, error) {
	if !cmd.Flags().Changed(b.flag) {
		return "", false, nil
	}
	if b.isSet != nil {
		return strconv.FormatBool(*b.isSet), true, nil
	}
	v := b.text.value
	switch {
	case v == "-":
		data, err := io.ReadAll(s.in)
		return string(data), true, err
	case strings.HasPrefix(v, "@"):
		data, err := os.ReadFile(v[1:])
		if err != nil {
			return "", false, usageError{fmt.Errorf("--%s: %w", b.flag, err)}
		}
		return string(data), true, nil
	}
	return v, true, nil
}

func (s *state) readSecret(field string, args []string) (string, error) {
	if !s.stdinTTY {
		data, err := io.ReadAll(s.in)
		if err != nil {
			return "", err
		}
		v := strings.TrimRight(string(data), "\r\n")
		if v == "" {
			return "", usageError{fmt.Errorf("pipe the %s to stdin", field)}
		}
		return v, nil
	}
	label := field
	if len(args) > 0 {
		label = args[0]
	}
	fmt.Fprintf(s.errOut, "%s (input is hidden): ", label)
	data, err := term.ReadPassword(int(s.in.Fd()))
	fmt.Fprintln(s.errOut)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", usageError{fmt.Errorf("no %s given", field)}
	}
	return string(data), nil
}

// convert checks a value against its type and returns it as JSON would carry it.
func convert(p Param, raw string) (any, error) {
	bad := func(want string) error {
		return usageError{fmt.Errorf("--%s takes %s, not %q", flagName(p), want, raw)}
	}
	switch p.Type {
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, bad("a whole number")
		}
		return n, nil
	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, bad("a number")
		}
		return n, nil
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, bad("true or false")
		}
		return b, nil
	case "array":
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out, nil
	case "json":
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, bad("JSON")
		}
		return v, nil
	}
	if len(p.Enum) > 0 && !contains(p.Enum, raw) {
		return nil, bad("one of " + strings.Join(p.Enum, ", "))
	}
	return raw, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// wrap word-wraps each paragraph to width, keeping list items' hanging indent.
func wrap(text string, width int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if len(line) <= width || strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "|") {
			out = append(out, line)
			continue
		}
		hang := ""
		if strings.HasPrefix(line, "- ") {
			hang = "  "
		}
		cur := ""
		for _, word := range strings.Fields(line) {
			if cur != "" && len(cur)+1+len(word) > width {
				out = append(out, cur)
				cur = hang + word
				continue
			}
			if cur == "" {
				cur = word
			} else {
				cur += " " + word
			}
		}
		out = append(out, cur)
	}
	return strings.Join(out, "\n")
}
