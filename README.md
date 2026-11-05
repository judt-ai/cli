# judt

The Judt CLI. It manages [Judt](https://judt.ai) Apps from the command line: create
them, change their source, preview, commit, deploy, set Secrets, and read logs.

```bash
npx judt auth login
npx judt apps create --name "Acme hiring"
npx judt apps deploy
```

## Install

| How | Command |
|---|---|
| npm (any OS) | `npm install -g judt`, or run it with `npx judt` |
| Script (macOS, Linux) | `curl -fsSL https://judt.ai/install \| sh` |
| Go | `go install github.com/judt-ai/cli/cmd/judt@latest` |

Every path installs the same single binary. `judt upgrade` prints the command that
updates yours.

## Sign in

```bash
judt auth login                         # opens your browser
judt auth login --device                # a code to enter on another device
echo "$TOKEN" | judt auth login --with-token
export JUDT_API_TOKEN=judt_...          # CI and agents; wins over any sign-in
judt whoami
```

## Use

`judt --help` and every `--help` below it say what each command does. Every
command is one call to the [Judt API](https://api.judt.ai/api/openapi.json), and its
help names the call. Add `--json` for the API's JSON.

```bash
judt apps use app_...                   # the current App, saved in .judt.json
judt apps files write src/index.ts --content @src/index.ts
judt apps run "npm test"
judt apps preview
judt apps commit "Add the pricing page"
judt apps deploy
pbpaste | judt apps secrets set STRIPE_SECRET_KEY
judt apps logs --level error
judt api GET /api/apps                  # anything else
```

| Environment | |
|---|---|
| `JUDT_API_TOKEN` | API token to use instead of the saved sign-in |
| `JUDT_APP` | The App to act on |
| `JUDT_PROFILE` | The saved sign-in to use (`--profile`) |
| `JUDT_API_URL` | Another API, such as staging |
| `JUDT_CONFIG_DIR` | Where sign-ins and settings live (default `~/.config/judt`) |
| `JUDT_NO_KEYRING` | Store sign-ins in a file instead of the OS keychain |
| `JUDT_NO_UPDATE_CHECK` | Don't check for new versions |

## Develop

Commands are generated from the API's OpenAPI document (`spec/openapi.json`) and
`commands.yaml`, which names each operation's command and writes the group help.

```bash
go generate ./...                 # regenerate internal/cli/operations_gen.go
go run ./internal/gen -check      # what CI runs: every operation named, file current
go test ./...
go build -o bin/judt ./cmd/judt
```

When the API adds an operation, `spec-sync` opens a pull request and CI fails until
`commands.yaml` names it: add one entry with its `command`, `section` and an
`example`. Changed parameters and descriptions need nothing.

Release: push a tag `vX.Y.Z`. `release.yml` runs GoReleaser (archives and
checksums) and `npm/publish.sh` (the `judt` package and one
`@judt/cli-<platform>-<arch>` package per binary). npm needs the `NPM_TOKEN`
secret; without it, the release skips npm.

The design is in Core's `docs/cli-proposal.md`.
