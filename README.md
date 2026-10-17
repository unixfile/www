# mdserve

Browse trees of Markdown docs in the browser, rendered on each request, with their git history.

## Usage

```
mdserve [-fnh] [-a ip[:port]] [dir]
mdserve [-fnh] --share[=host[:port]] [--secret[=token]] [dir]
mdserve --restart [-a ip[:port] | --share[=host[:port]]]
mdserve -l [-a ip[:port] | --share[=host[:port]]] [dir]
mdserve --kill [-a ip[:port] | --share[=host[:port]]]
```

Run `mdserve` in a directory. It prints the tree's URL, opens the browser and writes a lockfile named after the endpoint, such as `127.31.1.1-8080.lock`.
`cat` the lockfile to see the URL, root, PID and start time.
Delete the lockfile to stop serving the tree. The Delete lock button on the tree's pages does the same.
The URL is all it prints, so `url=$(mdserve -n)` works. Running `mdserve` again in a served directory prints the same URL and starts nothing.

Trees on one address share one server. The first `mdserve` starts it in the background, and later ones hand their tree to it and return.
When something other than your mdserve on that address holds the port, `mdserve` fails at once with `E98` and names the listeners there.
The server exits when its last lock is gone. SIGTERM stops it at once and removes every lock, as does the Kill server button on the index at `/`.
A running server keeps the binary it started with. `mdserve --restart` moves every server's trees to the current binary, and `make install` and `make install-deb` run it for you; open tabs reload onto the new binary. A binary without the capability for port 80, such as the one from `make install`, leaves a port-80 server on its old binary and says so.

The index at `/` lists the server's locks: each tree with the time its lock was written and a Delete lock button, and the lock's path on hover. Trees on other addresses follow as plain links, since only their own server may stop them. The header shows when the server started, and a warning appears when the binary on disk has changed since.
To reach the server by name, add a line such as `127.31.1.1 mdserve` to `/etc/hosts`. Type the name with its slash, eg `mdserve/`, or the browser searches for it.

- `-a`, `--addr ip[:port]`: serve on another loopback address; the port defaults to 80 where mdserve may bind it, else 8080
- `--share[=host[:port]]`: share the tree on the default route's address, or on the address or name given beyond loopback, such as `192.168.1.5`, `0.0.0.0` or `mate.peer.home`; see [sharing](doc/sharing.md)
- `--secret[=token]`: serve a shared tree under a random token, or the one given, so only those holding the link reach it
- `-f`, `--foreground`: when no server runs on the address yet, serve in the foreground until Ctrl-C
- `-n`, `--no-open`: print the URL without opening a browser
- `--stamp`: show the binary's build time, the seconds since, its commit and the page's render time, small and faint, above each page's header. `make run` sets it; `--restart` leaves it off
- `--restart`: replace every server with one running this binary, serving the same trees, and print each tree's URL; with `-a` or `--share`, only the server on that address
- `-l`, `--list`: print each served tree's URL and directory, on every address; with `-a` or `--share`, on that address only; with a directory, only the trees at or below it
- `--kill`: stop every server and print the trees it served, as `--list` does; with `-a` or `--share`, only the server on that address
- `-h`, `--help`: print the options

Options follow GNU style. Short ones combine, as in `-fn`, and options may follow the directory. Optional values go joined, as in `--share=192.168.1.5`; with a space the address would be read as the directory, and mdserve says so.

Two variables set the default addresses. `--help` shows each value and where it comes from, and an error from a bad value names its variable.

- `MDSERVE_SHARE`: the address or name for a bare `--share`, eg `mate.peer.home`, in a start and in `--kill`, `--restart` and `--list`. Without it a bare `--share` takes the default route's address
- `MDSERVE_ADDR`: the loopback address for a start without `-a`. `--list`, `--kill` and `--restart` without an address still cover every server

## Sharing

`mdserve --share` serves the tree to other machines on the LAN from a server of its own. It serves only what other users could read, hides dot entries such as `.git` and shows nothing of the machine around the tree. `--secret` puts the tree under a random token. See [doc/sharing.md](doc/sharing.md).

## History

Add `?log` to a path for its git history, newest first: one line per commit, which opens to the full message and the diff. A tracked directory's listing opens it from its first entry, `<(git log)`, and a tracked doc or code page from a line at its top that names its last commit. A commit's hash opens its diff for that path, with a link to all its files. Shares show it without trailers, and only when other users could read the repo. See [doc/history.md](doc/history.md).

## Log

mdserve logs every run, server event and page request to the systemd journal: `journalctl --user -t mdserve`. Without a journal it writes `~/.local/state/mdserve/log`. See [doc/log.md](doc/log.md).

## Install

mdserve needs git.
`make install` links `build/mdserve` into `~/.local/bin`.
`make deb` builds a Debian package in `build/`, such as `mdserve_0~git20261015223930.20f02fd_amd64.deb`, versioned by the last commit's UTC time and hash. It depends on git. For another architecture, pass `ARCH`, eg `make deb ARCH=arm64`.
`make install-deb` builds the package while sudo asks for the password, then installs it with `sudo apt install`, which pulls in git. It first removes the `~/.local/bin` link from `make install`, which would otherwise shadow the package's binary. Run it again to replace an installed build, even of the same commit. `make uninstall-deb` removes the package.

To try a change without installing it, `make run` builds `build/mdserve` and serves this directory in the foreground on port 8080 with `--stamp`, beside an installed server on port 80. Stop it with Ctrl-C before the next run; a run that finds a server on 8080 hands its tree to that server, which keeps the old code.

The package grants `/usr/bin/mdserve` one capability, `cap_net_bind_service`, so it can bind port 80 while running as the user. `getcap /usr/bin/mdserve` shows it. With it mdserve serves on port 80 and URLs carry no port; without it, as after `make install`, the port is 8080. mdserve also takes port 80 where the machine lets every user bind low ports.

## Notes

Each user owns 127.31.N.0/24 with N = uid − 1000 and serves on 127.31.N.1 by default, port 80 or 8080 as above. Uids from 1000 to 1255 fit; others need `--addr`. `-a` takes only loopback addresses; `--share` takes the others.
The addresses keep users out of each other's way, not out of each other's trees. Loopback is open to every local account, so on a machine shared with other users, they can reach your server. Serve nothing there you would not show them.

URLs start with the served directory's path relative to home, eg `http://127.31.1.1:8080/scratch/github.com/unixfile/webstack/index.md`, so a URL names its tree. A directory outside home uses its absolute path.
Below the prefix, URLs mirror the file tree, so relative links in the docs work unchanged. An absolute link such as `/x.md` points outside the prefix and gives 404. A `.md` file renders as a page, a directory as a listing of its names, a text file as a highlighted code page with linkable lines, and anything else raw. Add `?raw` to any URL for the file itself. A tree inside another served tree answers for its own paths.
A code page anchors lines as GitHub does, so `main.go#L42` lands on line 42 and highlights it. Bare URLs in a doc become links.

Open pages reload when what they show changes. A doc or code page reloads when its file is written, a listing when a name in it changes, any page in a repo when its HEAD moves, and the index when a tree joins or leaves. A page whose file or tree is gone waits two seconds for it to return, then reloads to say so. A page whose server stops reloads after three seconds without it, so the browser says the server is gone; a restart takes about 60 ms, and its new server reloads the page first. Each page holds a server-sent events stream while its tab is visible; a hidden tab lets go, since a browser opens at most six connections to one host, and checks again when shown. The server watches the directory of each open page and its repo's git directory through inotify, since editors save by renaming a new file over the old one, and polls every second where inotify fails.

Docs follow CommonMark with GitHub's extensions, rendered by goldmark: tables, strikethrough, task lists, bare URLs as links, footnotes and definition lists. Lines join into a paragraph, and a blank line starts the next. A line that starts with a dash begins a list even right after text. Straight quotes and dashes turn typographic. Code in a fenced block with a known language is highlighted by chroma, as are code pages, whose language follows the file name. Every gap between blocks is the same, whether the block is a paragraph, list, table or code. Headings sit close to their text.

mdserve refuses to serve the home directory. A tree exposes everything below it, and home holds `~/.ssh`, `~/.gnupg` and every token file.
Symlinks inside the tree are followed, absolute ones included. A link that leads out of the tree is followed only into a directory that holds a file named `.mdserve-follow`, or below one, and only when other users could read the target. A file needs the others-read bit and a directory others-read and others-execute, checked from the marker's directory down. So nobody reads through mdserve what `cat` would not show them.
A store of private files linked into repos, such as lord's `~/.local/share/lord`, opens with one marker in its root. Secrets kept there at mode 600 stay refused. Opening a refused link shows the link, its target and the reason, and the listing marks it refused with the same on hover.
A loopback server answers only to loopback addresses, `localhost` and names that `/etc/hosts` maps to loopback. Its Content-Security-Policy forbids every script but mdserve's own, which reloads pages and opens history folds, and which it allows by hash. Raw HTML in a doc passes through, so a README in a cloned repo could otherwise run code on this origin.

Each endpoint has a registry directory such as `~/.local/state/mdserve/127.31.1.1-8080/` with a symlink to each lock its server holds. A joining `mdserve` adds a link, and the server sees it through inotify, writes the lock and serves the tree. Where inotify fails the server polls every second. An flock on the directory keeps a tree from joining a server on its way out. `$XDG_STATE_HOME` replaces `~/.local/state` when set.

A lock left by a crash is detected on the next start through its PID and the kernel boot ID, then removed. A new server also clears what a crashed server left in the registry.

The stylesheet is mdserve's own. It began as md2html-default's, whose light palette it keeps, and the page colours sit as variables on `:root`.
Pages load no fonts from the network. STIX Two Text, the serif, comes from the machine when installed and otherwise from the copy built into mdserve, which the browser caches for a year. The mono face is Iosevka where installed, else the system monospace.
With `--stamp`, each page says when its binary was built and how many seconds ago, the commit it was built from and how long the page took to render, eg `built 12:49, 108 s ago, from 1aa282c+modified, rendered in 3.3 ms`. It sits small and faint above the header, so a glance tells which build served the page. `+modified` marks a build from uncommitted code. mdserve's script counts the seconds up while the page is open. Shares leave the stamp out.
The header names a tree by its base name and shows the full path on hover. Every tree page's breadcrumbs start at `index`, which links `/`. The header, listings and mdserve's own messages are mono; documents keep the serif.
Every page keeps one width, 58ch of the body font, and keeps space for a scrollbar, so the header holds still from page to page. Code is half the size of the body text, never under 11px, so about 115 columns fit; longer lines scroll.

Add `*.*.*.*-*.lock` to `~/.config/git/ignore` so locks stay out of `git status`.

## Licence

MIT, see `LICENSE`. The embedded STIX Two Text fonts are under the SIL Open Font License, in `assets/fonts/STIXTwoText-OFL.txt`.
The binary links in three Go modules, all under the MIT licence: goldmark by Yusuke Inuzuka, chroma by Alec Thomas and regexp2 by Doug Clark, which ports parts of Microsoft's dotnet/corefx. The package's copyright file carries their notices.
