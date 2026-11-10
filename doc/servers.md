# Servers and locks

Run `www` in a directory. It prints the tree's URL, opens the browser and writes a lockfile named after the endpoint, such as `127.31.1.1-8080.lock`. `cat` the lockfile to see the URL, root, PID and start time.

The URL is all it prints, so `url=$(www --no-open)` works. Running `www` again in a served directory prints the same URL and starts nothing.

Delete the lockfile to stop serving the tree. The Delete lock button on the tree's pages does the same.

## One server per address

Trees on one address share one server. The first `www` starts it in the background, and later ones hand their tree to it and return.

When something other than your www on that address holds the port, `www` fails at once with `E98` and names the listeners there.

The server exits when its last lock is gone. SIGTERM stops it at once and removes every lock but a persistent `.LOCK`, as does the Kill server button on the index at `/`.

Delete lock and Kill server answer 410 Gone. The page shows the code and "Gone" on the line below it, then names each tree stopped, its lock removed or kept, and whether the server serves on.

## Restarts

A running server keeps the binary it started with. `www --restart` moves every server's trees to the current binary, and `make install` and `make install-deb` run it for you. Open tabs reload onto the new binary, and it says how many did.

It moves only the servers its own binary started, so the package and a build from `make install` leave each other's servers alone. It names each server it leaves, and `-f` takes them too. `make install-deb` passes `-f`, since it removes the `make install` link.

A binary without the capability for port 80, such as the one from `make install`, leaves a port-80 server on its old binary and says so.

## The index

The index at `/` reads the same on every server. It lists each running server in the order of their addresses, with the address and the binary it runs. Each server's trees follow, with the lock's path on hover.

The server you are reading from says "this page". Only its trees carry a Delete lock button, since a server takes a stop only from its own pages. Another server's address links to its own index.

The package's binary and the one `make install` links are listed under No server when they run none, so both installs always show. A warning appears under a server whose binary changed on disk after it started, with the command that moves it to the new one.

With `--debug`, each server also says when its binary was built, from which commit and when the server started, and each tree when its lock was written. These serve debugging, so they are hidden otherwise.

To reach the server by name, add a line such as `127.31.1.1 www` to `/etc/hosts`. Type the name with its slash, eg `www/`, or the browser searches for it.

## Addresses

Each user owns 127.31.N.0/24 with N = uid − 1000. www serves on 127.31.N.1 by default, on port 80 where it may bind it, else 8080, and online repos on 127.31.N.2.

Uids from 1000 to 1255 fit; others need `--addr`. `-a` takes only loopback addresses; `--share` takes the others.

## Serving after reboot

`www -p` writes a lock in capitals, such as `127.31.1.1-8080.LOCK`. It holds no PID, and it outlives its server: Kill server, `--kill`, SIGTERM and a reboot all stop the server but leave the `.LOCK` in place.

The next server on that address serves the tree again. So does `www --restart`, which starts a server for every address whose persistent trees have none.

Deleting the `.LOCK`, or Delete lock on the tree's pages, ends the tree for good. `www -p` in a tree served by a plain `.lock` moves it to a `.LOCK`, and a plain `www` in a tree that holds a `.LOCK` keeps it.

## Systemd units

A systemd user unit runs `www --restart` at login. Both ship disabled. They serve from login to logout, and linger stays off.

- The package puts `www.service` in `/usr/lib/systemd/user/`. Enable it with `systemctl --user enable www`.
- `make install` puts `www-dev.service` in `~/.config/systemd/user/`. Enable it with `systemctl --user enable www-dev`.

Each brings back only the trees its own binary served last, so with both installs both units run.

The package also ships a system template, `www@.service`, which serves a user's trees from boot, before login, with the capability for port 80. Enable it with `sudo systemctl enable www@<user>`.

While the template or the package's user unit runs for a user, the other stands aside, since both run one binary on the same trees. `www-dev` runs beside either.

## Registry

Each endpoint has a registry directory such as `~/.local/state/www/127.31.1.1-8080/` with a symlink to each lock its server holds. `$XDG_STATE_HOME` replaces `~/.local/state` when set.

A joining `www` adds a link, and the server sees it through inotify, writes the lock and serves the tree. Where inotify fails the server polls every second. An flock on the directory keeps a tree from joining a server on its way out.

A lock left by a crash is detected on the next start through its PID and the kernel boot ID, then removed. A new server also clears what a crashed server left in the registry.

A lock also names the server's PID namespace. Inside a sandbox or container, another namespace's PID says nothing, so www takes that server to be running: it keeps the lock, lists the tree and refuses `--kill` and `--restart` with E1. If that server has crashed, its lock stays until a reboot or until you delete it. A lock written before locks named their namespace counts as the host's. A `www` that cannot read its own namespace from `/proc` treats every lock this way.

## A base path

`--base=PATH` puts every URL of a new server below PATH, eg `www --base=/www` serves `~/notes` at `/www/notes/`. It is for a proxy that gives www one part of a domain, eg `example.org/www/`, beside other sites. The proxy passes the path on whole, base included.

The base covers everything www writes and answers: trees, the index at `/www/`, the stylesheet and fonts, the stop and bug forms, redirects and the web address's repos. `/www` alone redirects to `/www/`. A path outside the base is a 404.

A base is one or more segments of letters, digits, `-`, `.` and `_`, and no segment starts with `_`.

A base belongs to a server, not to a tree. A tree that joins a running server takes the server's base, and www says so when `--base` named another. `--restart` keeps each server's base, so it takes no `--base`; to change one, stop the server with `--kill` and start it again.
