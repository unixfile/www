# www

Browse trees of Markdown docs in the browser, rendered on each request, with their git history.

It is fast. A page from a running server takes one to three milliseconds, a server starts in about 40 ms, and a test in `make test` fails any page that takes over five.

## Usage

```
www [-fnph] [-a ip[:port]] [dir]
www [-fnph] --share[=host[:port]] [--secret[=token]] [dir]
www --restart [-a ip[:port] | --share[=host[:port]]]
www -l [-a ip[:port] | --share[=host[:port]]] [dir]
www --kill [-a ip[:port] | --share[=host[:port]]]
```

Run `www` in a directory. It prints the tree's URL, opens the browser and writes a lockfile named after the endpoint, such as `127.31.1.1-8080.lock`.
`cat` the lockfile to see the URL, root, PID and start time.
Delete the lockfile to stop serving the tree. The Delete lock button on the tree's pages does the same.
The URL is all it prints, so `url=$(www -n)` works. Running `www` again in a served directory prints the same URL and starts nothing.

Trees on one address share one server. The first `www` starts it in the background, and later ones hand their tree to it and return.
When something other than your www on that address holds the port, `www` fails at once with `E98` and names the listeners there.
The server exits when its last lock is gone. SIGTERM stops it at once and removes every lock but a persistent `.LOCK`, as does the Kill server button on the index at `/`.
A running server keeps the binary it started with. `www --restart` moves every server's trees to the current binary, and `make install` and `make install-deb` run it for you; open tabs reload onto the new binary. A binary without the capability for port 80, such as the one from `make install`, leaves a port-80 server on its old binary and says so.

The index at `/` lists the server's locks: each tree with the time its lock was written and a Delete lock button, and the lock's path on hover. Trees on other addresses follow as plain links, since only their own server may stop them. The header shows when the server started, and a warning appears when the binary on disk has changed since.
To reach the server by name, add a line such as `127.31.1.1 www` to `/etc/hosts`. Type the name with its slash, eg `www/`, or the browser searches for it.

- `-a`, `--addr ip[:port]`: serve on another loopback address; the port defaults to 80 where www may bind it, else 8080
- `--share[=host[:port]]`: share the tree on the default route's address, or on the address or name given beyond loopback, such as `192.168.1.5`, `0.0.0.0` or `mate.peer.home`; see [sharing](doc/sharing.md)
- `--secret[=token]`: serve a shared tree under a random token, or the one given, so only those holding the link reach it
- `-f`, `--foreground`: when no server runs on the address yet, serve in the foreground until Ctrl-C
- `-n`, `--no-open`: print the URL without opening a browser
- `-p`, `--persist`: write a persistent lock, such as `127.31.1.1-8080.LOCK`, so the tree is served again after a reboot; see [Serving after reboot](#serving-after-reboot)
- `--stamp`: show the binary's build time, the seconds since, its commit and the page's render time, small and faint, above each page's header. `make run` sets it; `--restart` leaves it off
- `--cache size`: memory a new server may spend on cached git answers, eg `64M`; `0` caches nothing. The default is `32M`, and `--restart` goes back to it
- `--disk size`: disk a new server may fill with online repos, eg `4G`; it drops the repos read longest ago. The default is `2G`, and `--restart` goes back to it
- `--restart`: replace every server with one running this binary, serving the same trees, and print each tree's URL; an address with persistent trees and no server gets one; with `-a` or `--share`, only the server on that address
- `-l`, `--list`: print each served tree's URL and directory, on every address; with `-a` or `--share`, on that address only; with a directory, only the trees at or below it
- `--kill`: stop every server and print the trees it served, as `--list` does; with `-a` or `--share`, only the server on that address
- `--version`: print when the binary was built and the commit it came from, as the stamp says it, eg `www built 2026-10-21 12:49, from 1aa282c+modified`
- `-h`, `--help`: print the options

Options follow GNU style. Short ones combine, as in `-fn`, and options may follow the directory. Optional values go joined, as in `--share=192.168.1.5`; with a space the address would be read as the directory, and www says so.

Two variables set the default addresses. `--help` shows each value and where it comes from, and an error from a bad value names its variable.

- `WWW_SHARE`: the address or name for a bare `--share`, eg `mate.peer.home`, in a start and in `--kill`, `--restart` and `--list`. Without it a bare `--share` takes the default route's address
- `WWW_ADDR`: the loopback address for a start without `-a`. `--list`, `--kill` and `--restart` without an address still cover every server

A third sets how a start opens its URL. `WWW_OPEN` holds the command, split at spaces, with `%s` where the URL goes, or the URL appended when no `%s` is given. `WWW_OPEN='brave-browser --app=%s'` opens a tree in an app window and `WWW_OPEN=firefox` in Firefox. It applies to loopback starts only; a share opens through `xdg-open`, as every start does without the variable.

## Serving after reboot

`www -p` writes a lock in capitals, such as `127.31.1.1-8080.LOCK`. It holds no PID, and it outlives its server: Kill server, `--kill`, SIGTERM and a reboot all stop the server but leave the `.LOCK` in place. The next server on that address serves the tree again, and so does `www --restart`, which starts a server for every address whose persistent trees have none. Deleting the `.LOCK`, or Delete lock on the tree's pages, ends the tree for good. `www -p` in a tree served by a plain `.lock` moves it to a `.LOCK`, and a plain `www` in a tree that holds a `.LOCK` keeps it.

A systemd user unit runs `www --restart` at login. `make install` puts it in `~/.config/systemd/user/` and the package in `/usr/lib/systemd/user/`, both disabled; enable it with `systemctl --user enable www`. It serves from login to logout, and linger stays off.
The package also ships a system template, `www@.service`, which serves a user's trees from boot, before login, with the capability for port 80. Enable it with `sudo systemctl enable www@<user>`. While either unit runs for a user, the other stands aside, since both would serve the same trees.

## Online repos

The server on your default address also serves 127.31.N.2, where a URL names a repo on a forge, eg `http://127.31.1.2/github.com/junegunn/fzf/`. The first visit fetches it into `~/.cache/www/` while a page shows each step, and www then serves it as it serves a checkout. A path that names no repo, eg `/github.com/junegunn`, lists the repos there. See [online repos](doc/online-repos.md).

## Sharing

`www --share` serves the tree to other machines on the LAN from a server of its own. It serves only what other users could read, hides dot entries such as `.git` and shows nothing of the machine around the tree. `--secret` puts the tree under a random token. See [doc/sharing.md](doc/sharing.md).

## History

Add `?log` to a path for its git history, newest first: one line per commit, which opens to the full message and the diff. A tracked directory's listing opens it from its first entry, `<(git log)`, and a tracked doc or code page from a line at its top that names its last commit. A commit's hash opens its diff for that path, with a link to all its files. Shares show it without trailers, and only when other users could read the repo. See [doc/history.md](doc/history.md).

## Log

www logs every run, server event and page request to the systemd journal: `journalctl --user -t www`. Without a journal it writes `~/.local/state/www/log`. See [doc/log.md](doc/log.md).

## Install

www needs git.
`make install` links `build/www` into `~/.local/bin`.
`make deb` builds a Debian package in `build/`, such as `www_0~git20261015223930.20f02fd_amd64.deb`, versioned by the last commit's UTC time and hash. It depends on git. For another architecture, pass `ARCH`, eg `make deb ARCH=arm64`.
`make install-deb` builds the package while sudo asks for the password, then installs it with `sudo apt install`, which pulls in git. It first removes the `~/.local/bin` link from `make install`, which would otherwise shadow the package's binary. Run it again to replace an installed build, even of the same commit. `make uninstall-deb` removes the package.

To try a change without installing it, `make run` builds `build/www` and serves this directory in the foreground on port 8080 with `--stamp`, beside an installed server on port 80. Stop it with Ctrl-C before the next run; a run that finds a server on 8080 hands its tree to that server, which keeps the old code.

The program's name lives in the file `WORKING_NAME`, since www is a working name until a final one is chosen. The binary, its paths, environment variables, journal tag and URLs, the Makefile and the Debian package all take it from there, and a test fails on any other copy outside prose and `go.mod`.

The package grants `/usr/bin/www` one capability, `cap_net_bind_service`, so it can bind port 80 while running as the user. `getcap /usr/bin/www` shows it. With it www serves on port 80 and URLs carry no port; without it, as after `make install`, the port is 8080. www also takes port 80 where the machine lets every user bind low ports.

## Notes

Each user owns 127.31.N.0/24 with N = uid − 1000 and serves on 127.31.N.1 by default, port 80 or 8080 as above, and online repos on 127.31.N.2. Uids from 1000 to 1255 fit; others need `--addr`. `-a` takes only loopback addresses; `--share` takes the others.
The addresses keep users out of each other's way, not out of each other's trees. Loopback is open to every local account, so on a machine shared with other users, they can reach your server. Serve nothing there you would not show them.

URLs start with the served directory's path relative to home, eg `http://127.31.1.1:8080/scratch/github.com/unixfile/webstack/index.md`, so a URL names its tree. A directory outside home uses its absolute path.
Below the prefix, URLs mirror the file tree, so relative links in the docs work unchanged. An absolute link such as `/x.md` points outside the prefix and gives 404. A `.md` file renders as a page, a directory as a listing of its names, a text file as a highlighted code page with linkable lines, and anything else raw. Add `?raw` to any URL for the file itself. A tree inside another served tree answers for its own paths.
The box under the header searches what is below it as you type, ignoring case. It holds the keyboard, so typing on any page searches, wherever focus is. On a listing it searches the directory: names that hold the query first, then lines in text files, with the directory's own entries at full strength and what lies in its subdirectories after a gap, dimmed. A line result opens a code page at its line. Any other page filters itself: a code page keeps its matching lines with their numbers, a doc its matching blocks, a history its matching commits, with each match marked, and Enter jumps to the first match in the whole page. Below, after a gap and dimmed, follow the directory's results without the page's own file. The query stays in the URL as `?q=`, so Back from a result or a reload shows the results again. Hidden files are searched too, but not the stores machines write, such as `.git`, `.venv` or `.cache`, nor files over the code page limit; a search stops after half a second and says so, and a share searches only what it serves. Escape clears the query and brings the page back. Up and Down move a highlight through a listing's entries, or through a search's matches and results, and Enter opens it; a match opens at its place in the whole page. Escape on an empty query goes up, eg from a file to its directory or from a commit to its history, and highlights the page it left, so Enter goes back. Left and Right move the cursor in the box and never navigate. With nothing to walk, Up, Down and Space scroll the page. The highlight survives a reload. On a touch screen the box takes the keyboard only when tapped, since it would raise the on-screen keyboard. Shift+Tab in the box enters the header at the crumb Escape goes to, and Tab goes on from there to raw and Delete lock. Escape on a focused link or button returns to the box. Beside the box, copy path copies the absolute path on disk of the page's file or directory, ready for an editor; a share has no such button. A code page anchors lines as GitHub does, so `main.go#L42` lands on line 42 and highlights it. Bare URLs in a doc become links. A relative link in a doc whose target is missing gets a dotted red underline and names the missing path on hover. A link that climbs out of the tree is not checked, since another tree may answer there.

A server keeps git's answers so most pages run no git at all: which repo each directory lies in, the histories pages show and the commits they open, which never change and are kept by their full hash. It reads HEAD from the repo's files, and a history is kept under the commit HEAD names, so a new commit misses the cache and nothing shown goes stale. A `.git` that appears or goes makes the server ask git again. `--cache` sets the memory for it.
The cache holds only what someone has visited, with one exception. When a tree joins, the server asks git in the background for what its listing and its README show, since those open first. The log says how long that took.
In a large repo git can take a second for a history, because it reads every commit back to the ones that touched the path. When it takes over 100 ms, the history page says so and names `git commit-graph write --reachable --changed-paths`, which builds git's index of the paths each commit changed and makes such histories several times faster. www never writes to the repo itself.

Open pages reload when what they show changes. A doc takes its new version in place, without a reload, and each block that changed fills with the highlight colour and fades over three seconds, so the eye finds the change after a save. A code page reloads when its file is written, a listing when a name in it changes, any page in a repo when its HEAD moves, and the index when a tree joins or leaves. A page whose file or tree is gone waits two seconds for it to return, then reloads to say so. A page whose server stops reloads after three seconds without it, so the browser says the server is gone; a restart takes about 60 ms, and its new server reloads the page first. Each page holds a server-sent events stream while its tab is visible; a hidden tab lets go, since a browser opens at most six connections to one host, and checks again when shown. The server watches the directory of each open page and its repo's git directory through inotify, since editors save by renaming a new file over the old one, and polls every second where inotify fails.

Docs follow CommonMark with GitHub's extensions, rendered by goldmark: tables, strikethrough, task lists, bare URLs as links, footnotes and definition lists. Lines join into a paragraph, and a blank line starts the next. A line that starts with a dash begins a list even right after text. Straight quotes and dashes turn typographic. A fenced block marked `dot` or `graphviz` becomes a diagram, drawn by Graphviz's `dot` when it is installed and kept by the hash of its source; without `dot`, or when `dot` rejects the graph, the block shows as code under a line that says why. `dot` may not read image files the graph names. Code in a fenced block with a known language is highlighted by chroma, as are code pages, whose language follows the file name. Go is lexed by Go's own scanner from the standard library instead, about 150 times faster, with the same colours. Python, JavaScript and shell have lexers of www's own, which follow chroma's rules and give every byte the class chroma gives it; chroma took 180 ms to lex argparse.py, 2,633 lines, and they take 2 ms. A whole page of it renders in about 5 ms, and jquery.js, 10,907 lines, in 28 ms instead of 400. Other languages go through chroma, at about 70 µs a line. The server keeps each coloured page by its content's hash, within 16 MB, so a later view takes a few milliseconds. Every gap between blocks is the same, whether the block is a paragraph, list, table or code. Headings sit close to their text.

www refuses to serve the home directory and every directory above it, such as `/home` and `/`. A tree exposes everything below it, and home holds `~/.ssh`, `~/.gnupg` and every token file.
Symlinks inside the tree are followed, absolute ones included. A link that leads out of the tree is followed only into a directory that holds a file named `.www-follow`, or below one, and only when other users could read the target. A file needs the others-read bit and a directory others-read and others-execute, checked from the marker's directory down. So nobody reads through www what `cat` would not show them.
A store of private files linked into repos, such as lord's `~/.local/share/lord`, opens with one marker in its root. Secrets kept there at mode 600 stay refused. Opening a refused link shows the link, its target and the reason, and the listing marks it refused with the same on hover.
A loopback server answers only to loopback addresses, `localhost` and names that `/etc/hosts` maps to loopback. Its Content-Security-Policy forbids every script but www's own, which reloads pages, marks changed blocks, walks listings by arrow and opens history folds, and which it allows by hash. Raw HTML in a doc passes through, so a README in a cloned repo could otherwise run code on this origin.
www runs no code a tree supplies. It reads files and runs git with every option that would start a program named in the repo's config turned off: the pager, textconv, external diff, signature checks, fsmonitor and fetching.

Each endpoint has a registry directory such as `~/.local/state/www/127.31.1.1-8080/` with a symlink to each lock its server holds. A joining `www` adds a link, and the server sees it through inotify, writes the lock and serves the tree. Where inotify fails the server polls every second. An flock on the directory keeps a tree from joining a server on its way out. `$XDG_STATE_HOME` replaces `~/.local/state` when set.

A lock left by a crash is detected on the next start through its PID and the kernel boot ID, then removed. A new server also clears what a crashed server left in the registry.

The stylesheet is www's own. It began as md2html-default's, whose light palette it keeps, and the page colours sit as variables on `:root`. A page turns dark when the OS asks for it through `prefers-color-scheme`, with a dark palette and syntax colours of its own; it is a first draft. The browser loads it once: its URL names a hash of its content, eg `/_www/style.7b94a08e7731.css`, so it is kept for a year, and a changed stylesheet comes under a new URL.
Pages load no fonts from the network. STIX Two Text, the serif, comes from the machine when installed and otherwise from the copy built into www, which the browser caches for a year. The mono face is Iosevka where installed, else the system monospace.
With `--stamp`, each page says when its binary was built and how many seconds ago, the commit it was built from and how long the page took to render, eg `built 12:49, 108 s ago, from 1aa282c+modified, rendered in 3.3 ms`. It sits small and faint above the header, so a glance tells which build served the page. `+modified` marks a build from uncommitted code. www's script counts the seconds up while the page is open. Shares leave the stamp out.
The header names a tree by its base name and shows the full path on hover. Every tree page's breadcrumbs start at `index`, which links `/`. The header, listings and www's own messages are mono; documents keep the serif.
Every page keeps one width, 58ch of the body font, and keeps space for a scrollbar, so the header holds still from page to page. Code is half the size of the body text, never under 11px, so about 115 columns fit; longer lines scroll.

Add `*.*.*.*-*.lock` and `*.*.*.*-*.LOCK` to `~/.config/git/ignore` so locks stay out of `git status`.

## Licence

MIT, see `LICENSE`. The embedded STIX Two Text fonts are under the SIL Open Font License, in `assets/fonts/STIXTwoText-OFL.txt`.
The binary links in three Go modules, all under the MIT licence: goldmark by Yusuke Inuzuka, chroma by Alec Thomas and regexp2 by Doug Clark, which ports parts of Microsoft's dotnet/corefx. The package's copyright file carries their notices.
