package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
)

// print writes a result: the API's JSON when piped or with --json, else text.
func (s *state) print(op *Operation, body []byte) error {
	var code int
	if op != nil && op.Exit != "" {
		var r map[string]any
		if json.Unmarshal(body, &r) == nil {
			if n, ok := r[op.Exit].(float64); ok {
				code = int(n)
			}
		}
	}
	if s.jsonOut || !s.stdoutTTY {
		s.printJSON(body)
	} else {
		s.printText(op, body)
	}
	if code != 0 {
		return exitError{code: code}
	}
	return nil
}

func (s *state) printJSON(body []byte) {
	if s.stdoutTTY {
		var buf bytes.Buffer
		if json.Indent(&buf, body, "", "  ") == nil {
			body = buf.Bytes()
		}
	}
	s.out.Write(bytes.TrimRight(body, "\n"))
	fmt.Fprintln(s.out)
}

func (s *state) printText(op *Operation, body []byte) {
	v, err := decodeOrdered(body)
	if err != nil {
		s.out.Write(body)
		return
	}
	obj, isObj := v.(object)
	if op != nil && op.Print != "" && isObj {
		if text, ok := obj.get(op.Print).(string); ok {
			fmt.Fprint(s.out, text)
			if !strings.HasSuffix(text, "\n") && text != "" {
				fmt.Fprintln(s.out)
			}
			if obj.get("truncated") == true {
				fmt.Fprintln(s.errOut, "(output truncated; run with --json for the whole result)")
			}
			return
		}
	}
	if isObj {
		if results, ok := obj.get("results").([]any); ok {
			s.printTable(results)
			if obj.get("has_more") == true {
				if cursor, ok := obj.get("next_cursor").(string); ok {
					fmt.Fprintf(s.errOut, "More results: add --cursor %s, or --all for every page.\n", cursor)
				}
			}
			return
		}
	}
	w := tabwriter.NewWriter(s.out, 0, 2, 2, ' ', 0)
	flatten(w, "", v)
	w.Flush()
}

// preferred are the columns a list shows, in order, when its rows have them.
var preferred = []string{"id", "time", "name", "slug", "title", "path", "size", "status", "sandbox_status", "level", "source", "kind", "subject", "url", "commit_sha", "message", "created_at"}

func (s *state) printTable(rows []any) {
	if len(rows) == 0 {
		fmt.Fprintln(s.errOut, "No results.")
		return
	}
	first, _ := rows[0].(object)
	var cols []string
	for _, c := range preferred {
		if first.has(c) && len(cols) < 6 {
			cols = append(cols, c)
		}
	}
	if len(cols) < 2 {
		cols = nil
		for _, kv := range first {
			if _, nested := kv.v.(object); !nested && len(cols) < 6 {
				cols = append(cols, kv.k)
			}
		}
	}
	w := tabwriter.NewWriter(s.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, strings.ToUpper(strings.Join(cols, "\t")))
	for _, r := range rows {
		row, _ := r.(object)
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = cell(row.get(c), 60)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	w.Flush()
}

func flatten(w *tabwriter.Writer, prefix string, v any) {
	switch t := v.(type) {
	case object:
		for _, kv := range t {
			key := kv.k
			if prefix != "" {
				key = prefix + "." + kv.k
			}
			if child, ok := kv.v.(object); ok && len(child) > 0 {
				flatten(w, key, child)
				continue
			}
			fmt.Fprintf(w, "%s\t%s\n", key, cell(kv.v, 0))
		}
	default:
		fmt.Fprintln(w, cell(v, 0))
	}
}

func cell(v any, max int) string {
	var s string
	switch t := v.(type) {
	case nil:
		s = "-"
	case string:
		s = t
	case float64:
		s = fmt.Sprint(t)
	case bool:
		s = fmt.Sprint(t)
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = cell(item, 0)
		}
		s = strings.Join(parts, ", ")
		if len(t) == 0 {
			s = "-"
		}
	case object:
		data, _ := json.Marshal(t)
		s = string(data)
	}
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len([]rune(s)) > max {
		s = string([]rune(s)[:max-1]) + "…"
	}
	return s
}

// object is a JSON object that keeps its keys' order.
type object []struct {
	k string
	v any
}

func (o object) get(k string) any {
	for _, kv := range o {
		if kv.k == k {
			return kv.v
		}
	}
	return nil
}

func (o object) has(k string) bool {
	for _, kv := range o {
		if kv.k == k {
			return true
		}
	}
	return false
}

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(kv.k)
		v, err := json.Marshal(kv.v)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func decodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	return decodeValue(dec)
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			var o object
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o = append(o, struct {
					k string
					v any
				}{kt.(string), v})
			}
			_, err := dec.Token()
			if o == nil {
				o = object{}
			}
			return o, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token()
			return list, err
		}
	}
	return tok, nil
}
