# Options and environment

`www --help` prints the options in short. Here is each in full.

- `-a ADDR`, `--addr=ADDR`: serve on another loopback address, an IP or IP:PORT; the port defaults to 80 where www may bind it, else 8080
- `--share[=HOST]`: share the tree on the default route's address, or on the address or name given beyond loopback, with an optional port, such as `192.168.1.5`, `0.0.0.0` or `mate.peer.home`; see `doc/sharing.md`
- `--secret`: serve a shared tree under the token in `WWW_SECRET`, or a random one, so only those holding the link reach it
- `--foreground`: when no server runs on the address yet, serve in the foreground until Ctrl-C
- `--no-open`: print the URL without opening a browser
- `--no-bug`: hide the bug button at the foot of each page and refuse reports; `--restart` passes it to the new servers when given
- `-p`, `--persist`: write a persistent lock, such as `127.31.1.1-8080.LOCK`, so the tree is served again after a reboot; see `doc/servers.md`
- `--debug`: show what serves debugging: above each page's header, the build stamp, see `doc/debugging.md`; on the index, each server's build and start time and each lock's time. `make run` sets it; `--restart` passes it to the new servers when given, and leaves it off otherwise
- `--cache=SIZE`: memory a new server may spend on cached git answers, eg `64M`; `0` caches nothing. The default is `32M`, and `--restart` goes back to it
- `--disk=SIZE`: disk a new server may fill with online repos, eg `4G`; it drops the repos read longest ago, but not one that another server is fetching, since every server shares the cache. The default is `2G`, and `--restart` goes back to it
- `--restart`: replace every server this binary started with one running it now, serving the same trees, and print each tree's URL; an address with persistent trees and no server gets one, when this binary served it last; with `-a` or `--share`, only the server on that address, whichever binary runs it; with a directory, it also serves that directory there and opens it in the browser unless a page of it was open, and says which
- `-f`, `--force`: with `--restart`, take the servers and persistent trees of other binaries too
- `-l`, `--list`: print each served tree's directory, URL and binary as keyval, on every address; with `-a` or `--share`, on that address only; with a directory, only the trees at or below it. A directory that holds no served tree prints nothing; one that does not exist fails with `E2`
- `--kill`: stop every server and print the trees it served, as `--list` does; with `-a` or `--share`, only the server on that address
- `--commit=REV`: print as keyval the commit REV names in the repo of DIR, or of the current directory: its full hash, its stardate and its subject. REV is a stardate, eg `261024.4`, a hash or any name git takes, so `git show $(www --commit=261024.4 | sed -n 's/^hash //p')` shows a commit seen on a page. A day and x, eg `261026.x`, works while one commit alone holds it. A commit or repo that is not there fails with `E2`
- `-V`, `--version`: print when the binary was built and the commit it came from, as the stamp says it, eg `www built 2026-10-21 12:49, from 1aa282c+modified`
- `-h`, `--help`: print the options

Options follow GNU style. Short ones combine, as in `-pa 127.31.1.2`, and options may follow the directory. Optional values go joined, as in `--share=192.168.1.5`; with a space the address would be read as the directory, and www says so. `-f` and `-n` mean force and dry run in every tool of the author's, so `--foreground` and `--no-open` have no short form. The help text is written in docopt notation, except that docopt has no way to write the optional `=HOST`.

`--list` and `--kill` print [keyval](https://unixfile.github.io/keyval/): three lines per tree, sorted, with every index the same width. The binary line is missing for a server that started before www recorded it.

```
tree.0.binary /usr/bin/www
tree.0.dir /home/t/keep/notes
tree.0.url http://127.31.1.1/keep/notes/
tree.1.binary /home/t/scratch/github.com/unixfile/www/build/www
tree.1.dir /home/t/scratch/github.com/unixfile/www
tree.1.url http://127.31.1.1:8080/scratch/github.com/unixfile/www/
```

Two variables set the default addresses. `--help` shows each value and where it comes from, and an error from a bad value names its variable.

- `WWW_SHARE`: the address or name for a bare `--share`, eg `mate.peer.home`, in a start and in `--kill`, `--restart` and `--list`. Without it a bare `--share` takes the default route's address
- `WWW_ADDR`: the loopback address for a start without `-a`. `--list`, `--kill` and `--restart` without an address still cover every server

A third sets how a start opens its URL. `WWW_OPEN` holds the command, split at spaces, with `%s` where the URL goes, or the URL appended when no `%s` is given. `WWW_OPEN='brave-browser --app=%s'` opens a tree in an app window and `WWW_OPEN=firefox` in Firefox. It applies to loopback starts only; a share opens through `xdg-open`, as every start does without the variable.

A fourth holds a share's token. `WWW_SECRET` gives `--secret` a token of letters, digits, `-` and `_` instead of a random one. The token never goes on the command line, where `ps` shows it to every local user and the shell keeps it in its history, so `--secret=token` fails with `E22`. `--help` says whether the variable is set but never shows its value.
