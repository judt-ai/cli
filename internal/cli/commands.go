package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/judt-ai/cli/internal/api"
	"github.com/judt-ai/cli/internal/auth"
	"github.com/judt-ai/cli/internal/config"
)

func (s *state) addAuthCommands(group *cobra.Command) {
	var device, withToken bool
	login := &cobra.Command{
		Use:   "login [flags]",
		Short: "Sign in to Judt",
		Long: wrap(`Signs the CLI in to one Judt organization and saves the sign-in in the OS keychain (or in credentials.json in the config directory where there is no keychain).

With no flags, it opens your browser: sign in, pick the organization, and approve the CLI. Over SSH, or where no browser can open, it uses --device on its own.

--device prints a code to enter at the device page on any phone or computer.

--with-token reads an API key from stdin, or asks for it without showing it. Create API keys in the dashboard. JUDT_API_KEY, when set, is used instead of any saved sign-in.

To sign in to another organization, add --profile <name>.`, 80),
		Example: examples([]string{
			"judt auth login",
			"judt auth login --device",
			"echo \"$API_KEY\" | judt auth login --with-token",
			"judt auth login --profile work",
		}),
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if withToken {
				return s.loginWithToken(cmd.Context())
			}
			return s.loginOAuth(cmd.Context(), device || noBrowser())
		},
	}
	login.Flags().BoolVar(&device, "device", false, "Sign in with a code entered on another device, instead of opening a browser here.")
	login.Flags().BoolVar(&withToken, "with-token", false, "Read an API key from stdin, instead of signing in with a browser.")
	login.MarkFlagsMutuallyExclusive("device", "with-token")

	logout := &cobra.Command{
		Use:     "logout",
		Short:   "Sign out, and forget the saved sign-in",
		Long:    wrap("Revokes the profile's OAuth sign-in on the server and deletes it from this computer. An API key saved with --with-token is only forgotten here; revoke it in the dashboard.", 80),
		Example: examples([]string{"judt auth logout", "judt auth logout --profile work"}),
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile := s.profileName()
			cred, err := config.LoadCredential(profile)
			if err != nil {
				return err
			}
			if cred == nil {
				fmt.Fprintf(s.errOut, "Profile %s isn't signed in.\n", profile)
				return nil
			}
			if cred.Type == "oauth" && cred.RefreshToken != "" {
				if err := auth.Revoke(context.Background(), s.httpClient(), cred.RevocationEndpoint, cred.ClientID, cred.RefreshToken); err != nil {
					fmt.Fprintln(s.errOut, "Warning: couldn't revoke the sign-in on the server:", err)
				}
			}
			if err := config.DeleteCredential(profile); err != nil {
				return err
			}
			fmt.Fprintf(s.errOut, "Signed out of profile %s.\n", profile)
			if os.Getenv("JUDT_API_KEY") != "" {
				fmt.Fprintln(s.errOut, "JUDT_API_KEY is still set, so commands still use it.")
			}
			return nil
		},
	}
	group.AddCommand(login, logout)
}

func (s *state) loginWithToken(ctx context.Context) error {
	var token string
	if s.stdinTTY {
		fmt.Fprint(s.errOut, "API key (input is hidden): ")
		data, err := term.ReadPassword(int(s.in.Fd()))
		fmt.Fprintln(s.errOut)
		if err != nil {
			return err
		}
		token = string(data)
	} else {
		data, err := io.ReadAll(s.in)
		if err != nil {
			return err
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return usageError{fmt.Errorf("no key given: pipe an API key to judt auth login --with-token")}
	}
	cred := &config.Credential{Type: "api_token", APIURL: s.baseURL(nil), Token: token}
	return s.finishLogin(ctx, cred, s.tokenClient(cred.APIURL, token))
}

func (s *state) loginOAuth(ctx context.Context, device bool) error {
	apiURL := s.baseURL(nil)
	server, err := auth.Discover(ctx, s.httpClient(), apiURL)
	if err != nil {
		return err
	}
	var token *auth.Token
	if device {
		d, err := auth.StartDevice(ctx, s.httpClient(), server)
		if err != nil {
			return err
		}
		link := d.VerificationURIComplete
		if link == "" {
			link = d.VerificationURI
		}
		fmt.Fprintf(s.errOut, "To sign in, open this page on any device:\n\n  %s\n\nand check that it shows the code %s. Waiting for you to approve…\n", link, d.UserCode)
		token, err = auth.PollDevice(ctx, s.httpClient(), server, d)
		if err != nil {
			return exitError{4, "Sign-in failed: " + err.Error()}
		}
	} else {
		token, err = auth.Browser(ctx, s.httpClient(), server, func(u string) {
			fmt.Fprintf(s.errOut, "Opening your browser to sign in. If it doesn't open, go to:\n\n  %s\n\n", u)
			_ = openURL(u)
		})
		if err != nil {
			return exitError{4, "Sign-in failed: " + err.Error()}
		}
	}
	cred := &config.Credential{
		Type: "oauth", APIURL: apiURL,
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: token.ExpiresAt,
		ClientID: server.ClientID, TokenEndpoint: server.TokenEndpoint, RevocationEndpoint: server.RevocationEndpoint, Resource: server.Resource,
	}
	return s.finishLogin(ctx, cred, s.tokenClient(apiURL, token.AccessToken))
}

// finishLogin checks the credential with GET /api/me, saves it, and says who it is.
func (s *state) finishLogin(ctx context.Context, cred *config.Credential, client *api.Client) error {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := client.Do(ctx, api.Request{Method: "GET", Path: "/api/me"})
	if err != nil {
		return err
	}
	var me struct {
		User         struct{ Name, Email string }
		Organization struct{ Name string }
		Role         string
	}
	_ = json.Unmarshal(resp.Body, &me)
	if err := config.SaveCredential(s.profileName(), cred); err != nil {
		return err
	}
	if s.jsonOut || !s.stdoutTTY {
		s.printJSON(resp.Body)
	}
	fmt.Fprintf(s.errOut, "Signed in to %s as %s (%s), %s. Profile: %s.\n", me.Organization.Name, me.User.Name, me.User.Email, me.Role, s.profileName())
	return nil
}

// noBrowser is true where a browser can't open for this terminal: over SSH, or on
// Linux without a display.
func noBrowser() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return true
	}
	return runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
}

func openURL(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}

func (s *state) addAppCommands(group *cobra.Command) {
	use := &cobra.Command{
		Use:     "use <app-id>",
		Short:   "Make an App the current one in this directory",
		Long:    wrap("Checks that the App exists, then saves its ID in .judt.json in the current directory. Commands below judt apps, run here or in a directory below, act on it unless --app or JUDT_APP names another.", 80),
		Example: examples([]string{"judt apps use app_...", "judt apps list   # to find an App's ID"}),
		GroupID: "manage",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{fmt.Errorf("judt apps use takes one App ID")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client()
			if err != nil {
				return err
			}
			resp, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: "/api/apps/" + url.PathEscape(args[0])})
			if err != nil {
				return err
			}
			var app struct{ ID, Name, URL string }
			_ = json.Unmarshal(resp.Body, &app)
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			p, err := config.WriteApp(dir, app.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(s.errOut, "Using %s (%s) in %s.\n", app.Name, app.ID, filepath.Dir(p))
			return nil
		},
	}
	open := &cobra.Command{
		Use:     "open [app-id]",
		Short:   "Open the App's production URL in your browser",
		Long:    wrap("Opens the App's production URL in your browser and prints it. Acts on the App you name, else the one that --app names, else JUDT_APP, else the nearest .judt.json. For the sandbox's dev server, run judt apps preview.", 80),
		Example: examples([]string{"judt apps open"}),
		GroupID: "manage",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageError{fmt.Errorf("judt apps open takes at most one App ID")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) == 1 {
				id = args[0]
			} else if id2, err := s.currentApp(); err != nil {
				return err
			} else {
				id = id2
			}
			client, err := s.client()
			if err != nil {
				return err
			}
			resp, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: "/api/apps/" + url.PathEscape(id)})
			if err != nil {
				return err
			}
			var app struct{ URL string }
			_ = json.Unmarshal(resp.Body, &app)
			fmt.Fprintln(s.out, app.URL)
			if s.stdoutTTY {
				_ = openURL(app.URL)
			}
			return nil
		},
	}
	group.AddCommand(use, open)
}

func (s *state) otherCommands() []*cobra.Command {
	var data, key string
	var query []string
	apiCmd := &cobra.Command{
		Use:   "api <method> <path> [flags]",
		Short: "Send any API request with your sign-in",
		Long: wrap(`Sends one request to the Judt API with the CLI's sign-in and prints the JSON answer. Use it for anything the other commands don't cover yet. The API reference is at https://docs.judt.ai, and the OpenAPI document at https://api.judt.ai/api/openapi.json.

A change (POST, PUT, PATCH, DELETE) gets an Idempotency-Key. If it fails with 503 or 504, run the same command again with the --idempotency-key it prints.`, 80),
		Example: examples([]string{
			"judt api GET /api/apps",
			"judt api GET /api/apps/app_.../logs -q level=error -q limit=20",
			"judt api POST /api/apps --data '{\"name\": \"Blog\"}'",
			"judt api PUT /api/apps/app_.../files/README.md --data @body.json",
		}),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return usageError{fmt.Errorf("judt api takes a method and a path, such as GET /api/apps")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			method, path := strings.ToUpper(args[0]), args[1]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			q := url.Values{}
			for _, kv := range query {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return usageError{fmt.Errorf("-q takes key=value, not %q", kv)}
				}
				q.Add(k, v)
			}
			req := api.Request{Method: method, Path: path, Query: q, IdempotencyKey: key}
			if data != "" {
				raw := data
				if raw == "-" {
					b, err := io.ReadAll(s.in)
					if err != nil {
						return err
					}
					raw = string(b)
				} else if strings.HasPrefix(raw, "@") {
					b, err := os.ReadFile(raw[1:])
					if err != nil {
						return usageError{err}
					}
					raw = string(b)
				}
				var body any
				if err := json.Unmarshal([]byte(raw), &body); err != nil {
					return usageError{fmt.Errorf("--data isn't JSON: %w", err)}
				}
				req.Body = body
			}
			if req.IdempotencyKey == "" && method != "GET" && method != "HEAD" {
				req.IdempotencyKey = api.NewKey()
			}
			client, err := s.client()
			if err != nil {
				return err
			}
			resp, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			s.printJSON(resp.Body)
			return nil
		},
	}
	apiCmd.Flags().StringVarP(&data, "data", "d", "", "The JSON request body. @path reads a file, - reads stdin.")
	apiCmd.Flags().StringArrayVarP(&query, "query", "q", nil, "A query parameter, as key=value. Repeat it for more.")
	apiCmd.Flags().StringVar(&key, "idempotency-key", "", "The Idempotency-Key to send, to retry an interrupted change.")

	version := &cobra.Command{
		Use:   "version",
		Short: "Print the CLI's version",
		Args:  noArgs,
		Run: func(*cobra.Command, []string) {
			if s.jsonOut {
				fmt.Fprintf(s.out, "{\"version\":%q,\"os\":%q,\"arch\":%q}\n", s.version, runtime.GOOS, runtime.GOARCH)
				return
			}
			fmt.Fprintf(s.out, "judt %s (%s/%s)\n", s.version, runtime.GOOS, runtime.GOARCH)
		},
	}
	upgrade := &cobra.Command{
		Use:   "upgrade",
		Short: "Show how to update the CLI to the latest version",
		Long:  wrap("Prints the command that updates this copy of judt, for the way it was installed: npm or the install script.", 80),
		Args:  noArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintln(s.out, upgradeCommand())
		},
	}
	return []*cobra.Command{apiCmd, version, upgrade}
}

func upgradeCommand() string {
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if strings.Contains(exe, "node_modules") {
		return "npm install -g judt@latest   (npx judt always runs the latest version)"
	}
	return "curl -fsSL https://judt.ai/install | sh"
}
