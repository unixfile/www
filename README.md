# mdserve

Browse a directory of Markdown docs in the browser, rendered by pandoc on each request.

## Usage

```
mdserve [-fg] [-nolock] [-nobreaks] [-noopen] [-port n] [dir]
```

Run `mdserve` in a directory. It starts in the background, prints its URL, opens the browser and writes a lockfile such as `serving-port-7417.lock`.
`cat` the lockfile to see the URL, root, PID and start time.
Delete the lockfile to stop the server. The Stop server button on every page does the same, as does SIGTERM.
Running `mdserve` again in a served directory prints the running server's URL instead of starting another.

- `-fg`: stay in the foreground until Ctrl-C
- `-nolock`: write no lockfile; implies `-fg`
- `-nobreaks`: join consecutive lines into one paragraph, as standard Markdown does
- `-noopen`: print the URL without opening a browser
- `-port n`: listen on port n; the default 7417 falls back to a free port when taken, an explicit port does not

`make install` links `build/mdserve` into `~/.local/bin`.

## Notes

URLs start with the served directory's path relative to home, eg `http://127.0.0.1:7417/scratch/github.com/unixfile/webstack/index.md`, so a URL names its tree. A directory outside home uses its absolute path. `/` redirects to that prefix.
Below the prefix, URLs mirror the file tree, so relative links in the docs work unchanged. An absolute link such as `/x.md` points outside the prefix and gives 404. A `.md` file renders as a page, a directory as a listing with its README below, a text file as a highlighted code page with linkable lines, and anything else raw. Add `?raw` to any URL for the file itself.
A code page anchors lines as GitHub does, so `main.go#L42` lands on line 42 and highlights it. Bare URLs in a doc become links.

Each source line renders as its own paragraph by default, through a small pandoc Lua filter. Lines of one block sit close together; a blank line still gives the full paragraph gap. The line-centric spec format puts one rule per line, which standard Markdown joins into one paragraph.

The server binds 127.0.0.1 only, answers only to localhost host names and forbids scripts through a Content-Security-Policy. Pandoc passes raw HTML through, so a README in a cloned repo could otherwise run code on this origin.

A lock left by a crash is detected on the next start through its PID and the kernel boot ID, then removed.

The stylesheet is a copy of md2html-default's. Changes there do not reach mdserve.

Add `serving-port-*.lock` to `~/.config/git/ignore` so locks stay out of `git status`.
