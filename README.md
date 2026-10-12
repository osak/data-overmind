# data-overmind

`data-overmind` is a collection of small data and productivity tools. Each tool has its own executable entry point while implementation details remain isolated from the other tools.

## Tools

| Tool | Description | Run |
| --- | --- | --- |
| `fileserver` | Finder-style browser for the current directory, with AI Doc Template review writeback | `just run fileserver` |
| `review-context` | Resolve an AI Doc Template review against its current HTML and emit compact agent context | `just run review-context path/to/report.html` |

The `fileserver` renders Markdown, text, images, audio, video, and PDFs in place. HTML files open directly in a new tab. Normal browsing remains read-only; the only write operation is the review API described below. Symlinks that resolve outside the startup directory are shown as unavailable and are never served.

## AI Doc Template reviews

Pages built with `ai-doc-template` automatically detect the file server. Each paragraph and list item gets a subtle comment action, drafts are kept in `localStorage`, and **レビューをサブミットする** writes a sibling JSON file:

```text
report.html
report.html.review.json
```

Run the server from the directory that contains the document, open the HTML through the displayed `http://127.0.0.1:8080/` URL, add comments, and submit. An AI can then read the HTML and its `.review.json` companion together. The review file contains the quoted paragraph, stable block identifiers, timestamps, and source hashes; it never modifies the HTML itself.

### Review version history

Every successful submission keeps an immutable copy of the review and a snapshot of the HTML bytes it reviewed. The sibling `.review.json` remains the latest review for existing tools. History lives beside the document:

```text
report.html
report.html.review.json
report.html.review-history/
  <source SHA-256 without prefix>/
    source.html
    reviews/
      <review SHA-256 without prefix>.json
```

The source hash identifies a document version; the review hash identifies an individual submission. Submitting another review of the same HTML adds a review file without changing earlier entries. Editing `report.html` and submitting again creates a new source directory. Keep the `.review-history` directory with the HTML when moving or backing up the document. The fileserver exposes it through its normal file browser.

Older `.review.json` files created before this history rule are preserved on the next submission. If the HTML had already changed, the old comments are archived under their recorded source hash, but the former HTML bytes cannot be reconstructed and that version has no `source.html` snapshot.

Review writes are limited to HTML files below the startup directory. Requests are same-origin, size-limited, schema-validated, and use source/review revisions to reject stale overwrites. Binding with `--addr 0.0.0.0:9000` also exposes the review write endpoint to reachable devices, so use the default loopback address unless LAN access is intentional.

The URL prefix `/_data-overmind/` is reserved for this API and takes precedence over files with the same path.

### Reading a review efficiently

Use `review-context` instead of passing the complete HTML and persistence JSON to an AI agent:

```sh
just run review-context workspace/report.html
just run review-context --format json workspace/report.html
just run review-context --review workspace/report.html.review-history/SOURCE_HASH/reviews/REVIEW_HASH.json workspace/report.html
```

The command finds `workspace/report.html.review.json` automatically and does not modify either file. Its default Markdown output contains each human comment, the current target text, the nearest reviewable blocks before and after it, and its heading hierarchy. Persistence-only fields such as timestamps, comment UUIDs, and full hashes are omitted.

Each comment has one of five target-resolution states:

- `exact`: saved block ID and text still match.
- `relocated`: the reviewed text exists once at a different block ID.
- `changed`: the block ID exists but its text changed; both reviewed and current text are shown.
- `ambiguous`: the reviewed text exists in multiple places, so no target is guessed.
- `missing`: no safe current target was found.

The document-level `Source: changed` marker means the HTML bytes differ from the reviewed version. Individual comments can still be `exact` when their targets did not change. `--format json` emits the same resolved information as compact JSON for programmatic use.

## Repository layout

```text
cmd/
  fileserver/          executable entry point
  review-context/      compact review reader for AI agents
internal/
  fileserver/          server, views, Markdown rendering, review API, and tests
  reviewcontext/       HTML/review matching and compact rendering
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
