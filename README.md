# mdserve

Browse trees of Markdown docs in the browser, rendered by pandoc on each request.

## Usage

```
mdserve [-fg] [-nolock] [-nobreaks] [-noopen] [-addr ip[:port]] [dir]
```

Run `mdserve` in a directory. It prints the tree's URL, opens the browser and writes a lockfile named after the endpoint, such as `127.31.1.1-8080.lock`.
`cat` the lockfile to see the URL, root, PID and start time.
Delete the lockfile to stop serving the tree. The Stop serving button on the tree's pages does the same.
Running `mdserve` again in a served directory prints the running server's URL instead of starting another.

Trees on one address share one server. The first `mdserve` starts it in the background, and later ones hand their tree to it and return.
The server exits when its last lock is gone. SIGTERM stops it at once and removes every lock, as does the Stop server button on the index at `/`.

- `-addr ip[:port]`: serve on another loopback address; the port defaults to 8080
- `-fg`: when no server runs on the address yet, serve in the foreground until Ctrl-C
- `-nolock`: write no lockfile and serve alone in the foreground; it never joins a server, so a taken address is an error
- `-nobreaks`: join consecutive lines into one paragraph, as standard Markdown does; a running server keeps the setting it started with
- `-noopen`: print the URL without opening a browser

`make install` links `build/mdserve` into `~/.local/bin`.

## Notes

Each user owns 127.31.N.0/24 with N = uid − 1000 and serves on 127.31.N.1:8080 by default. Uids from 1000 to 1255 fit; others need `-addr`. Only loopback addresses serve.

URLs start with the served directory's path relative to home, eg `http://127.31.1.1:8080/scratch/github.com/unixfile/webstack/index.md`, so a URL names its tree. A directory outside home uses its absolute path. `/` lists the served trees.
Below the prefix, URLs mirror the file tree, so relative links in the docs work unchanged. An absolute link such as `/x.md` points outside the prefix and gives 404. A `.md` file renders as a page, a directory as a listing with its README below, a text file as a highlighted code page with linkable lines, and anything else raw. Add `?raw` to any URL for the file itself. A tree inside another served tree answers for its own paths.
A code page anchors lines as GitHub does, so `main.go#L42` lands on line 42 and highlights it. Bare URLs in a doc become links.

Each source line renders as its own paragraph by default, through a small pandoc Lua filter. Lines of one block sit close together; a blank line still gives the full paragraph gap. The line-centric spec format puts one rule per line, which standard Markdown joins into one paragraph.

mdserve refuses to serve the home directory. A tree exposes everything below it, and home holds `~/.ssh`, `~/.gnupg` and every token file.
The server answers only to loopback addresses, `localhost` and names that `/etc/hosts` maps to loopback. It forbids scripts through a Content-Security-Policy. Pandoc passes raw HTML through, so a README in a cloned repo could otherwise run code on this origin.

Each endpoint has a registry directory such as `~/.local/state/mdserve/127.31.1.1-8080/` with a symlink to each lock its server holds. A joining `mdserve` adds a link, and the server sees it through inotify, writes the lock and serves the tree. Where inotify fails the server polls every second. An flock on the directory keeps a tree from joining a server on its way out. `$XDG_STATE_HOME` replaces `~/.local/state` when set.

A lock left by a crash is detected on the next start through its PID and the kernel boot ID, then removed. A new server also clears what a crashed server left in the registry.

The stylesheet is a copy of md2html-default's. Changes there do not reach mdserve.

Add `127.*-*.lock` to `~/.config/git/ignore` so locks stay out of `git status`.
