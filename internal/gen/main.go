// Command gen writes internal/cli/operations_gen.go from spec/openapi.json and
// commands.yaml: one Operation per API operation, with its flags, help and
// positional arguments, and the groups that hold them.
//
//	go run ./internal/gen          write the file; warn about unnamed operations
//	go run ./internal/gen -check   fail if an operation is unnamed, an entry is stale,
//	                               or the file is out of date (CI)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type entry struct {
	Command  string   `yaml:"command"`
	Args     []string `yaml:"args"`
	Section  string   `yaml:"section"`
	Aliases  []string `yaml:"aliases"`
	Example  []string `yaml:"example"`
	Secret   []string `yaml:"secret"`
	Print    string   `yaml:"print"`
	Exit     string   `yaml:"exit"`
	Confirm  bool     `yaml:"confirm"`
	Shortcut string   `yaml:"shortcut"`
	// Short and Help replace the spec's summary and description where those
	// describe HTTP details (headers, content types) that the CLI handles itself.
	Short string `yaml:"short"`
	Help  string `yaml:"help"`
}

type group struct {
	Section  string    `yaml:"section"`
	Aliases  []string  `yaml:"aliases"`
	Short    string    `yaml:"short"`
	Long     string    `yaml:"long"`
	Sections yaml.Node `yaml:"sections"`
}

type commands struct {
	Root struct {
		Long     string    `yaml:"long"`
		Example  []string  `yaml:"example"`
		Sections yaml.Node `yaml:"sections"`
	} `yaml:"root"`
	Groups     yaml.Node        `yaml:"groups"`
	Operations map[string]entry `yaml:"operations"`
}

type param struct {
	Name, Parent, Description, Type string
	Enum                            []string
	Required                        bool
}

type operation struct {
	entry
	ID, Method, Path, Summary, Description string
	AppArg, AppScoped, HasBody, Keyed      bool
	Query, Body                            []param
}

func main() {
	specPath := flag.String("spec", "spec/openapi.json", "the OpenAPI document")
	commandsPath := flag.String("commands", "commands.yaml", "the command names")
	out := flag.String("out", "internal/cli/operations_gen.go", "the file to write")
	check := flag.Bool("check", false, "fail instead of writing when anything is missing or stale")
	flag.Parse()

	src, problems, err := generate(*specPath, *commandsPath)
	if err != nil {
		fail(err.Error())
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "gen:", p)
	}
	if *check {
		current, _ := os.ReadFile(*out)
		if !bytes.Equal(current, src) {
			problems = append(problems, *out+" is out of date: run go generate ./...")
			fmt.Fprintln(os.Stderr, "gen:", problems[len(problems)-1])
		}
		if len(problems) > 0 {
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*out, src, 0o644); err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "gen:", msg)
	os.Exit(1)
}

var pathParam = regexp.MustCompile(`\{(\w+)\}`)

func generate(specPath, commandsPath string) ([]byte, []string, error) {
	var spec map[string]any
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", specPath, err)
	}
	var cmds commands
	raw, err = os.ReadFile(commandsPath)
	if err != nil {
		return nil, nil, err
	}
	if err := yaml.Unmarshal(raw, &cmds); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", commandsPath, err)
	}

	var problems []string
	var ops []operation
	seen := map[string]bool{}
	paths, _ := spec["paths"].(map[string]any)
	for path, item := range paths {
		methods, _ := item.(map[string]any)
		for method, v := range methods {
			raw, ok := v.(map[string]any)
			if !ok || method == "parameters" {
				continue
			}
			id, _ := raw["operationId"].(string)
			e, named := cmds.Operations[id]
			if !named {
				problems = append(problems, fmt.Sprintf("add %s (%s %s) to commands.yaml", id, strings.ToUpper(method), path))
				continue
			}
			seen[id] = true
			op, err := build(spec, id, strings.ToUpper(method), path, raw, e)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			ops = append(ops, op)
		}
	}
	for id := range cmds.Operations {
		if !seen[id] {
			problems = append(problems, fmt.Sprintf("commands.yaml names %s, which the spec doesn't have", id))
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Command < ops[j].Command })
	return render(cmds, ops), problems, nil
}

func build(spec map[string]any, id, method, path string, raw map[string]any, e entry) (operation, error) {
	op := operation{entry: e, ID: id, Method: method, Path: path}
	op.Summary, _ = raw["summary"].(string)
	op.Description, _ = raw["description"].(string)
	if e.Short != "" {
		op.Summary = e.Short
	}
	if e.Help != "" {
		op.Description = strings.TrimSpace(e.Help)
	}
	op.AppScoped = strings.HasPrefix(path, "/api/apps/{id}")

	for _, p := range list(raw["parameters"]) {
		p := deref(spec, p)
		name, _ := p["name"].(string)
		switch p["in"] {
		case "header":
			if name == "Idempotency-Key" {
				op.Keyed = true
			}
		case "query":
			schema := deref(spec, p["schema"])
			typ, enum := typeOf(spec, schema)
			desc, _ := p["description"].(string)
			if desc == "" {
				desc, _ = schema["description"].(string)
			}
			required, _ := p["required"].(bool)
			op.Query = append(op.Query, param{Name: name, Description: desc, Type: typ, Enum: enum, Required: required})
		}
	}
	sort.Slice(op.Query, func(i, j int) bool { return op.Query[i].Name < op.Query[j].Name })

	if body := deref(spec, dig(raw, "requestBody", "content", "application/json", "schema")); body != nil {
		op.HasBody = true
		required := set(list(body["required"]))
		props, _ := body["properties"].(map[string]any)
		for _, name := range keys(props) {
			s := deref(spec, props[name])
			if children, ok := s["properties"].(map[string]any); ok {
				childRequired := set(list(s["required"]))
				for _, child := range keys(children) {
					cs := deref(spec, children[child])
					typ, enum := typeOf(spec, cs)
					desc, _ := cs["description"].(string)
					op.Body = append(op.Body, param{Name: child, Parent: name, Description: desc, Type: typ, Enum: enum,
						Required: required[name] && childRequired[child]})
				}
				continue
			}
			typ, enum := typeOf(spec, s)
			desc, _ := s["description"].(string)
			op.Body = append(op.Body, param{Name: name, Description: desc, Type: typ, Enum: enum, Required: required[name]})
		}
	}

	pathNames := map[string]bool{}
	for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
		if !(m[1] == "id" && op.AppScoped) {
			pathNames[m[1]] = true
		}
	}
	bodyNames := map[string]bool{}
	for _, b := range op.Body {
		if b.Parent == "" {
			bodyNames[b.Name] = true
		}
	}
	var args []string
	for _, a := range e.Args {
		switch {
		case a == "id?" && op.AppScoped:
			op.AppArg = true
		case pathNames[a], bodyNames[a]:
			args = append(args, a)
			delete(pathNames, a)
		default:
			return op, fmt.Errorf("%s: args names %q, which is neither a path parameter nor a body field", id, a)
		}
	}
	if len(pathNames) > 0 {
		return op, fmt.Errorf("%s: list path parameters %v in args", id, keys(pathNames))
	}
	for _, s := range e.Secret {
		if !bodyNames[s] {
			return op, fmt.Errorf("%s: secret names %q, which isn't a body field", id, s)
		}
	}
	op.Args = args
	return op, nil
}

// typeOf maps a JSON Schema to the CLI's value types: string, integer, number,
// boolean, array (comma-separated strings) or json (anything else, given as JSON).
func typeOf(spec map[string]any, s map[string]any) (string, []string) {
	for _, key := range []string{"anyOf", "oneOf"} {
		for _, option := range list(s[key]) {
			o := deref(spec, option)
			if o["type"] != "null" {
				return typeOf(spec, o)
			}
		}
	}
	t := s["type"]
	if ts, ok := t.([]any); ok {
		for _, v := range ts {
			if v != "null" {
				t = v
				break
			}
		}
	}
	var enum []string
	for _, v := range list(s["enum"]) {
		if str, ok := v.(string); ok {
			enum = append(enum, str)
		}
	}
	switch t {
	case "string", "integer", "number", "boolean":
		return t.(string), enum
	case "array":
		if items := deref(spec, s["items"]); items != nil && items["type"] == "string" {
			return "array", nil
		}
	}
	return "json", nil
}

func deref(spec map[string]any, v any) map[string]any {
	m, _ := v.(map[string]any)
	for m != nil {
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		var cur any = spec
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cm, _ := cur.(map[string]any)
			cur = cm[part]
		}
		m, _ = cur.(map[string]any)
	}
	return nil
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		cm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = cm[p]
	}
	return cur
}

func list(v any) []any { l, _ := v.([]any); return l }

func set(l []any) map[string]bool {
	s := map[string]bool{}
	for _, v := range l {
		if str, ok := v.(string); ok {
			s[str] = true
		}
	}
	return s
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pairs reads a YAML mapping in file order.
func pairs(n yaml.Node) [][2]*yaml.Node {
	var out [][2]*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, [2]*yaml.Node{n.Content[i], n.Content[i+1]})
	}
	return out
}

func sections(n yaml.Node) string {
	var b strings.Builder
	b.WriteString("[]Section{")
	for _, kv := range pairs(n) {
		fmt.Fprintf(&b, "{ID: %s, Title: %s},", q(kv[0].Value), q(kv[1].Value))
	}
	b.WriteString("}")
	return b.String()
}

func render(cmds commands, ops []operation) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by internal/gen from spec/openapi.json and commands.yaml. DO NOT EDIT.\n\npackage cli\n\n")
	fmt.Fprintf(&b, "var rootHelp = Group{Long: %s, Example: %s, Sections: %s}\n\n", q(cmds.Root.Long), strs(cmds.Root.Example), sections(cmds.Root.Sections))

	b.WriteString("var groups = []Group{\n")
	for _, kv := range pairs(cmds.Groups) {
		var g group
		if err := kv[1].Decode(&g); err != nil {
			fail(fmt.Sprintf("group %s: %v", kv[0].Value, err))
		}
		fmt.Fprintf(&b, "{Path: %s, Section: %s, Aliases: %s, Short: %s, Long: %s, Sections: %s},\n",
			q(kv[0].Value), q(g.Section), strs(g.Aliases), q(g.Short), q(g.Long), sections(g.Sections))
	}
	b.WriteString("}\n\n")

	b.WriteString("var operations = []Operation{\n")
	for _, op := range ops {
		fmt.Fprintf(&b, "{\nID: %s, Command: %s, Method: %s, Path: %s,\n", q(op.ID), q(op.Command), q(op.Method), q(op.Path))
		fmt.Fprintf(&b, "Summary: %s,\nDescription: %s,\n", q(op.Summary), q(op.Description))
		fmt.Fprintf(&b, "Section: %s, Shortcut: %s, Aliases: %s, Example: %s,\n", q(op.Section), q(op.Shortcut), strs(op.Aliases), strs(op.Example))
		fmt.Fprintf(&b, "Args: %s, AppArg: %t, AppScoped: %t, HasBody: %t, Keyed: %t,\n", strs(op.Args), op.AppArg, op.AppScoped, op.HasBody, op.Keyed)
		fmt.Fprintf(&b, "Secret: %s, Print: %s, Exit: %s, Confirm: %t,\n", strs(op.Secret), q(op.Print), q(op.Exit), op.Confirm)
		fmt.Fprintf(&b, "Query: %s,\nBody: %s,\n},\n", params(op.Query), params(op.Body))
	}
	b.WriteString("}\n")

	src, err := format.Source(b.Bytes())
	if err != nil {
		fail("format: " + err.Error())
	}
	return src
}

func params(ps []param) string {
	if len(ps) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("[]Param{\n")
	for _, p := range ps {
		fmt.Fprintf(&b, "{Name: %s, Parent: %s, Type: %s, Enum: %s, Required: %t,\nDescription: %s},\n",
			q(p.Name), q(p.Parent), q(p.Type), strs(p.Enum), p.Required, q(p.Description))
	}
	b.WriteString("}")
	return b.String()
}

func q(s string) string { return strconv.Quote(s) }

func strs(l []string) string {
	if len(l) == 0 {
		return "nil"
	}
	parts := make([]string, len(l))
	for i, s := range l {
		parts[i] = q(s)
	}
	return "[]string{" + strings.Join(parts, ", ") + "}"
}
