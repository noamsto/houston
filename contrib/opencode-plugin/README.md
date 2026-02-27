# Houston Plugin for OpenCode

This plugin enables automatic discovery of OpenCode instances by houston.

## Installation

Copy `houston.ts` to your OpenCode plugins directory:

```bash
cp houston.ts ~/.config/opencode/plugins/
```

Or for project-specific:

```bash
cp houston.ts .opencode/plugins/
```

## How it works

When OpenCode starts, the plugin writes a JSON file to `~/.local/state/houston/opencode-servers/{pid}.json` containing:

- `pid`: Process ID
- `url`: Server URL (e.g., `http://127.0.0.1:4096`)
- `project`: Project name
- `directory`: Working directory
- `startedAt`: Timestamp

Houston reads these files to discover running OpenCode instances, regardless of what port they're using.

The plugin automatically cleans up:
- Its own file on exit (normal exit, SIGINT, SIGTERM)
- Stale files from crashed processes

## Requirements

- OpenCode 1.0+
- houston with OpenCode support enabled (default)
