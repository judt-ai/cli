// Package cli is the judt command tree: generated operation commands, the groups
// that hold them, and a few hand-written commands (sign-in, the current App, raw
// API calls).
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/judt-ai/cli/internal/api"
)

// state is one run's flags and streams.
type state struct {
	version             string
	profile, apiURL     string
	app                 string
	jsonOut             bool
	out, errOut         io.Writer
	in                  *os.File
	stdoutTTY, stdinTTY bool
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string) int {
	s := &state{
		version: version, out: os.Stdout, errOut: os.Stderr, in: os.Stdin,
		stdoutTTY: term.IsTerminal(int(os.Stdout.Fd())),
		stdinTTY:  term.IsTerminal(int(os.Stdin.Fd())),
	}
	root := s.newRoot()
	notice := s.startUpdateCheck()
	cmd, err := root.ExecuteC()
	notice()
	if err == nil {
		return 0
	}
	return s.report(cmd, err)
}

const inputNote = `
Flags that take text also take @path, to read a file, or -, to read stdin:
--content @src/index.ts.

Exit codes: 0 success, 1 the API refused or failed the request, 2 the command
was used wrongly, 4 not signed in or not allowed.`

func (s *state) newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "judt",
		Short:         "Manage Judt Apps from the command line",
		Long:          strings.TrimSpace(rootHelp.Long) + "\n" + inputNote,
		Example:       examples(rootHelp.Example),
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       s.version,
	}
	root.SetVersionTemplate("judt {{.Version}}\n")
	root.SetUsageTemplate(usageTemplate)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.PersistentFlags().BoolVar(&s.jsonOut, "json", false, "Print the API's JSON answer instead of text. Scripts and agents should use it.")
	root.PersistentFlags().StringVar(&s.profile, "profile", "", "The saved sign-in to use. Default: JUDT_PROFILE, else default.")
	root.PersistentFlags().StringVar(&s.apiURL, "api-url", "", "The API to call. Default: JUDT_API_URL, else the profile's, else "+api.DefaultURL+".")
	addSections(root, rootHelp.Sections)

	nodes := map[string]*cobra.Command{"": root}
	for _, g := range groups {
		c := &cobra.Command{
			Use:     lastWord(g.Path) + " <command>",
			Aliases: g.Aliases,
			Short:   g.Short,
			Long:    strings.TrimSpace(g.Long),
			GroupID: g.Section,
			Example: examples(g.Example),
			// Runnable, so cobra checks Args: an unknown subcommand is a usage error, and
			// the group alone prints its help.
			Args: noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		}
		addSections(c, g.Sections)
		nodes[parentPath(g.Path)].AddCommand(c)
		nodes[g.Path] = c
	}
	nodes["apps"].PersistentFlags().StringVar(&s.app, "app", "", "The App to act on, by ID. Default: JUDT_APP, else the nearest .judt.json.")

	for i := range operations {
		op := &operations[i]
		parent := nodes[parentPath(op.Command)]
		if parent == nil {
			panic("commands.yaml: no group for " + op.Command)
		}
		parent.AddCommand(s.operationCommand(op, lastWord(op.Command)))
		if op.Shortcut != "" {
			short := s.operationCommand(op, op.Shortcut)
			short.Aliases, short.GroupID = nil, rootHelp.Sections[0].ID
			short.Short = op.Summary + " (same as judt " + op.Command + ")"
			root.AddCommand(short)
		}
	}

	s.addAuthCommands(nodes["auth"])
	s.addAppCommands(nodes["apps"])
	other := rootHelp.Sections[len(rootHelp.Sections)-1].ID
	for _, c := range s.otherCommands() {
		c.GroupID = other
		root.AddCommand(c)
	}
	root.SetHelpCommandGroupID(other)
	root.SetCompletionCommandGroupID(other)
	return root
}

// usageTemplate is cobra's, with flag help wrapped to 100 columns and the API call
// each command makes at the end.
const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if and .HasAvailableSubCommands (not .Runnable)}}
  {{.CommandPath}} <command>{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}:{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

More commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsagesWrapped 100 | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global flags:
{{.InheritedFlags.FlagUsagesWrapped 100 | trimTrailingWhitespaces}}{{end}}{{if index .Annotations "api"}}

API call: {{index .Annotations "api"}}{{end}}{{if .HasAvailableSubCommands}}

Run "{{.CommandPath}} <command> --help" to see what a command does and its flags.{{end}}
`

func addSections(c *cobra.Command, sections []Section) {
	for _, sec := range sections {
		c.AddGroup(&cobra.Group{ID: sec.ID, Title: sec.Title})
	}
}

func examples(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return "  " + strings.Join(lines, "\n  ")
}

func lastWord(path string) string { return path[strings.LastIndex(path, " ")+1:] }

func parentPath(path string) string {
	if i := strings.LastIndex(path, " "); i >= 0 {
		return path[:i]
	}
	return ""
}

// noArgs makes a group answer an unknown subcommand with its help, not silence.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageError{fmt.Errorf("%q isn't a command of %s", args[0], cmd.CommandPath())}
	}
	return nil
}

// usageError is a command used wrongly: exit code 2.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }

// exitError ends the run with a code; msg, if any, is printed first.
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

var errNotSignedIn = exitError{4, "Not signed in. Run judt auth login, or set JUDT_API_TOKEN to an API token."}

func (s *state) report(cmd *cobra.Command, err error) int {
	var ee exitError
	var ue usageError
	var ae *api.Error
	switch {
	case errors.As(err, &ee):
		if ee.msg != "" {
			fmt.Fprintln(s.errOut, ee.msg)
		}
		return ee.code
	case errors.As(err, &ue):
		fmt.Fprintf(s.errOut, "Error: %s\nRun \"%s --help\" for usage.\n", ue.err, cmd.CommandPath())
		return 2
	case errors.As(err, &ae):
		fmt.Fprintf(s.errOut, "Error: %s: %s\n", ae.Code, ae.Message)
		if d := ae.DetailText(); d != "" {
			fmt.Fprintln(s.errOut, indent(d, "  "))
		}
		if (ae.Status == 503 || ae.Status == 504) && ae.IdempotencyKey != "" {
			fmt.Fprintf(s.errOut, "The work is recorded but not finished. To pick it up rather than repeat it, run the same command with:\n  --idempotency-key %s\n", ae.IdempotencyKey)
		}
		if ae.RequestID != "" {
			fmt.Fprintf(s.errOut, "Request ID: %s\n", ae.RequestID)
		}
		if ae.Status == 401 || ae.Code == "insufficient_scope" {
			fmt.Fprintln(s.errOut, "Sign in again with judt auth login, or check JUDT_API_TOKEN.")
			return 4
		}
		return 1
	default:
		msg := err.Error()
		if strings.HasPrefix(msg, "unknown command") || strings.HasPrefix(msg, "unknown flag") || strings.Contains(msg, "arg(s)") {
			fmt.Fprintf(s.errOut, "Error: %s\nRun \"%s --help\" for usage.\n", msg, cmd.CommandPath())
			return 2
		}
		fmt.Fprintln(s.errOut, "Error:", msg)
		return 1
	}
}

func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n"+prefix)
}
