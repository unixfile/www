# Testing

Two kinds of test, split by what a rewrite in another language would keep.

## Go tests

They check internals: parsers, edit distance, path math, the lexers against chroma. They also drive the handler in process, which is fast and covers most behavior. A rewrite deletes them.

Write new Go tests as tables, with the cases in `testdata/` where they fit. Purpose: a later move to black-box form then reuses the data.

## Black-box tests

They run the built binary and check what a user sees. They cover what in-process tests cannot reach: flag parsing, the listener, `--public`, process lifecycle and browser pages. The first case is a missing path on `--public`, which shows the 404 coin.

The suite is pytest in `tests/`, with its own `pyproject.toml`. `make blackbox` builds the binary and runs it. HTTP, browser and process cases exist so far; the CLI kind below comes with its first case.

- CLI: `subprocess` checks stdout, stderr and the exit code apart, including `E{code}` messages
- HTTP: `httpx` against a server the suite starts on a free port
- Browser: Playwright in `tests/test_browser.py`, asserting DOM, layout and computed style, never pixels. It uses Playwright's Chromium where installed, else the machine's Brave, and skips with the reason when no browser starts, eg in a sandbox. Playwright drives only its own Firefox build, so a Firefox check stays a script in `csi/sketch/browser-tests/`
- Processes: `subprocess.Popen` with signals and waits for locks, restarts and the journal, in `tests/test_processes.py`. A case that needs a new PID namespace skips with the reason where `unshare` is refused, eg in a sandbox

Rules for the suite:

- It reads the binary from `TEST_BIN` and assumes nothing else about it
- An HTTP or CLI case is a TOML file in `tests/cases/`: argv with expected stdout and exit code, or a request with the expected page. A parametrized test loads it
- A case names the server forms it runs on: `public`, the `--public` flag, or `default`, a reader's server, as wwwvm's unit runs it
- A case compares the page without its script and style, since a reader never sees their words
- `tests/harness.py` seeds the cache on disk, since a public server clones only over https from the open internet. It is the one place the suite knows the cache's layout
- Name each case after the decision it checks
- Make runs repeatable: git repos built with fixed dates, a throwaway `HOME` and `XDG_*`, a free port, no network
- Replace volatile parts in expected output before comparing: stardates, timestamps, ports, temp paths

## Race detector

Run `go test -race` when a change touches locks, restart, serve or the watcher.

## Heavy runs

Corpus crawl, differential lexing, fuzzing, mutation testing and soak runs go on the user's own machine. The model writes the script and its instructions in `csi/sketch/`. The user runs it and names the output path.

## Later

The large Go tests, starting with `web_test.go`, move to black-box form when a rewrite is decided. The old binary then serves as the check on the new cases, and the same suite runs against both.
