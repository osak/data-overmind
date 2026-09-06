# data-overmind

`data-overmind` is a collection of small data and productivity tools. Each tool has its own executable entry point while implementation details remain isolated from the other tools.

## Tools

| Tool | Description | Run |
| --- | --- | --- |
| `fileserver` | Read-only, Finder-style browser for the current directory | `just run fileserver` |

The `fileserver` renders Markdown, text, images, audio, video, and PDFs in place. HTML files open directly in a new tab. Symlinks that resolve outside the startup directory are shown as unavailable and are never served.

## Repository layout

```text
cmd/
  fileserver/          executable entry point
internal/
  fileserver/          server, views, Markdown rendering, and tests
Justfile               shared development tasks
```

New tools should use `cmd/<tool>/main.go`. Private application code belongs under `internal/<tool>`; code intended to be reusable outside this repository can be promoted to `pkg/<package>` when such a need appears.

## Development

```sh
just run fileserver                # run a named tool
just serve                         # shorthand for the file server
just serve --addr 0.0.0.0:9000    # pass arguments to a tool
just build                         # build fileserver to bin/fileserver
just build another-tool            # build cmd/another-tool
just generate                      # regenerate all templ output
just test                          # run all tests
just check                         # generate, format, and test
```

The file server listens on <http://127.0.0.1:8080/> by default. Binding to `0.0.0.0` exposes every file below the current directory to other devices that can reach the port.
