## devmux

Small, single-binary TUI process multiplexer for local development. Point it at a simple `procs.json` and it will start each command in its own pane, stream colorized output, and give you handy keybindings to focus, follow, clear, and reorder panes.

### Features

- **Multiple processes**: one scrollable pane per process
- **Color-aware output**: forces common color env vars so CLI colors render correctly
- **Easy navigation**: cycle focus, jump to top/bottom, clear pane
- **Reorder on the fly**: move panes up/down to keep important ones together
- **Graceful shutdown**: sends SIGTERM to full process groups (POSIX), then SIGKILL after a grace period
- **Works on macOS/Linux/Windows** (process-group semantics vary on Windows)
- **Copy-friendly UI**: borderless panes by default with optional one-line dividers

### Quick start

1) Create a `procs.json` in your working directory (see schema below). You can also copy the included `procs.json.dist` as a starting point.

```bash
cp procs.json.dist procs.json
```

2) Build and run (requires Go 1.24+ per `go.mod`).

```bash
go build -o devmux .
./devmux

# or run directly without building
go run .
```

If `procs.json` is missing or invalid, devmux will print an error and exit.

### Configuration: `procs.json`

Devmux looks for a file named `procs.json` in the current working directory.

Schema (per-process):
- **name** (string, required): label shown in the pane title
- **dir** (string, optional): working directory for the command
- **cmd** (string, required): executable to run
- **args** (string[], optional): arguments passed to the command
- **follow** (boolean, optional): auto-scroll to bottom; defaults to `true`

Example:

```json
{
  "processes": [
    {
      "name": "Portal (npm)",
      "dir": "./portal",
      "cmd": "npm",
      "args": ["run", "dev"],
      "follow": true
    },
    {
      "name": "API (Go)",
      "dir": "./api",
      "cmd": "go",
      "args": ["run", "."],
      "follow": true
    },
    {
      "name": "Temporal",
      "cmd": "temporal",
      "args": ["server", "start-dev"]
    }
  ]
}
```

### Keyboard shortcuts

- **TAB**: cycle focus across panes
- **Ctrl-L**: clear the focused pane
- **q / Q / Ctrl-C**: quit devmux
- **f**: toggle follow (auto-scroll) for focused pane
- **r**: reload focused process (SIGTERM → SIGKILL, then restart)
- **g**: scroll to top for focused pane
- **G**: scroll to bottom for focused pane
- **J**: move focused pane down
- **K**: move focused pane up

Mouse support is enabled for convenience.

### Behavior notes

- **Color output**: devmux sets `FORCE_COLOR=1`, `CLICOLOR=1`, `CLICOLOR_FORCE=1`, and `TERM=xterm-256color` for spawned processes to encourage colorized output.
- **Startup**: processes are launched shortly after the UI initializes so panes are ready to receive output immediately.
- **Header indicators**: the top status line shows the focused pane name and the current states of follow, borders, and dividers.
- **Borders/Dividers**: panes start borderless with a single-line divider between them to keep clipboard copies clean in tmux; toggle with `b` and `d`.
- **Shutdown**:
  - POSIX (macOS/Linux): sends signals to the child process group (SIGTERM, then SIGKILL after ~2s)
  - Windows: falls back to killing the child process
- **Signals**: SIGINT/SIGTERM to devmux will stop the UI and trigger process termination.

### Development

Prereqs: Go 1.24+

Build locally:

```bash
go build -o devmux .
```

Run from source:

```bash
go run .
```

### Why devmux?

Sometimes you just want a tiny, dependency-free tool that runs a handful of commands for a project (frontend, API, worker, services) with a clean terminal UI and graceful shutdown — no Procfile or DSL required, just straightforward JSON.

### Acknowledgements

Built with `tview` and `tcell` — thanks to those projects for the excellent terminal UI foundations.
