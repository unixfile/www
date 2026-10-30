# Testing

Two kinds of test, split by what a rewrite in another language would keep.

## Go tests

They check internals: parsers, edit distance, path math, the lexers against chroma. They also drive the handler in process, which is fast and covers most behavior. A rewrite deletes them.

Write new Go tests as tables, with the cases in `testdata/` where they fit. Purpose: a later move to black-box form then reuses the data.

## Black-box tests

They run the built binary and check what a user sees. They cover what in-process tests cannot reach: flag parsing, the listener, `--public`, process lifecycle and browser pages. The first case is a missing path on `--public`, which shows the 404 coin.

The suite is pytest in `tests/`, with its own `pyproject.toml`, run by `uv run pytest`:

- CLI: `subprocess` checks stdout, stderr and the exit code apart, including `E{code}` messages
- HTTP: `httpx` against a server the suite starts on a free port
- Browser: Playwright in Chromium and Firefox, asserting DOM and computed style, never pixels
- Processes: `subprocess.Popen` with signals and waits for locks, restarts and the journal

Rules for the suite:

- It reads the binary from `WWW_BIN` and assumes nothing else about it
- A case is a data file: argv with expected stdout and exit code, or a request with the expected page. A parametrized test loads it
- Name each case after the decision it checks
- Make runs repeatable: git repos built with fixed dates, a throwaway `HOME` and `XDG_*`, a free port, no network
- Replace volatile parts in expected output before comparing: stardates, timestamps, ports, temp paths

## Race detector

Run `go test -race` when a change touches locks, restart, serve or the watcher.

## Heavy runs

Corpus crawl, differential lexing, fuzzing, mutation testing and soak runs go on the user's own machine. The model writes the script and its instructions in `csi/sketch/`. The user runs it and names the output path.

## Later

The large Go tests, starting with `web_test.go`, move to black-box form when a rewrite is decided. The old binary then serves as the check on the new cases, and the same suite runs against both.
