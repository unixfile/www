# www

www reads a directory in the browser: Markdown as pages, code as highlighted source, a repo with its history. It runs on your machine and needs no build step.

It is for anyone who keeps notes, docs and code in plain files and wants to read them as linked pages, without pushing them anywhere first. A repo on GitHub, GitLab or Codeberg reads the same way once www has fetched it.

It is fast. A page from a running server takes one to three milliseconds, a server starts in about 40 ms, and a test in `make test` fails any page that takes over five.

## Usage

```
www [-p] [--foreground] [--no-open] [-a ADDR] [--debug] [--no-bug]
    [--cache=SIZE] [--disk=SIZE] [--] [DIR]
www [-p] [--foreground] [--no-open] --share[=HOST] [--secret] [--debug]
    [--no-bug] [--cache=SIZE] [--disk=SIZE] [--] [DIR]
www --restart [-f] [--no-open] [--debug] [--no-bug]
    [-a ADDR | --share[=HOST]] [--] [DIR]
www -l [-a ADDR | --share[=HOST]] [--] [DIR]
www --kill [-a ADDR | --share[=HOST]]
www --commit=REV [--] [DIR]
www --public --disk=SIZE (-a ADDR | --share[=HOST]) [--debug]
    [--cache=SIZE]
www -h | --help
www -V | --version
```

Run `www` in a directory. It prints the tree's URL, opens the browser and writes a lockfile named after the endpoint, such as `127.31.1.1-8080.lock`. Delete the lockfile to stop serving the tree. Trees on one address share one server, which exits when its last lock is gone.

```
www                         serve this directory and open it
www -p ~/notes              serve ~/notes, and again after a reboot
www --share=192.168.1.5     serve this directory to the LAN
www -l                      list every served tree as keyval
www --restart               move every server to the current binary
www --public --disk=6G -a 127.0.0.1:8080
                            serve online repos to the web, behind a proxy
```

Add `?log` to any page's URL for its history. Open `http://127.31.1.2/github.com/junegunn/fzf/` to read a repo from GitHub.

## Install

www needs git. `make install` links `build/www` into `~/.local/bin`, and serves on port 8080. `make install-deb` builds and installs the Debian packages, www and its syntax tree helper. The package's binary may bind port 80, so URLs carry no port.

## Documentation

- [Options and environment](doc/options.md): every option, the keyval output and the variables that set defaults
- [Servers and locks](doc/servers.md): how trees join a server, the index at `/`, addresses, restarts and serving after reboot
- [Pages](doc/pages.md): URLs, how docs render, links, live reload and the look
- [Code pages](doc/code-pages.md): highlighting, line links, scope boxes, the syntax tree and gron
- [Search and controls](doc/search.md): the search box, keys, the copy buttons and when controls fade
- [History](doc/history.md): histories, commits, stardates, the timeline and old versions of a tree
- [Online repos](doc/online-repos.md): reading a forge's repos on the second address, or serving them to the web
- [Sharing](doc/sharing.md): serving a tree to other machines
- [Safety](doc/safety.md): what www refuses to serve, follow or run
- [Speed](doc/speed.md): what www caches and how it is timed
- [Log](doc/log.md): what www writes to the journal
- [Debugging](doc/debugging.md): the bug button and the build stamp
- [Install](doc/install.md): the Debian package, port 80 and locks in git
- [Development](doc/development.md): `make run`, the working name and the usage check

## Licence

MIT, see `LICENSE`. The embedded STIX Two Text fonts are under the SIL Open Font License, in `assets/fonts/STIXTwoText-OFL.txt`.
The binary links in three Go modules, all under the MIT licence: goldmark by Yusuke Inuzuka, chroma by Alec Thomas and regexp2 by Doug Clark, which ports parts of Microsoft's dotnet/corefx. The package's copyright file carries their notices.
