# Sharing

`mdserve --share [dir]` serves a tree on the address of the default route, so other machines on the LAN can read it. mdserve asks the kernel which source address it would use to leave the machine, and the printed URL shows it. Over a full-tunnel VPN that is the tunnel's address.

`MDSERVE_SHARE`, eg `MDSERVE_SHARE=mate.peer.home`, gives a bare `--share` another address, for `--kill --share` and `--restart --share` as well.

`--share=ip[:port]` names the address instead, joined with `=`: `--share 192.168.1.5` would read the address as the directory. Any IPv4 address the machine lets mdserve bind will do, `0.0.0.0` included. A name will do too, such as `--share=mate.peer.home`: mdserve binds the address it resolves to, which an interface here must hold.

The printed URL carries the address's name when its reverse lookup resolves back to it, so a share on a WireGuard address with its own DNS prints `http://mate.peer.home/notes/`. Otherwise it carries the address. The port defaults to 80 where mdserve may bind it, else 8080, as on loopback. `-a` takes only loopback addresses, so a typo there cannot share a tree.

A shared address gets a server of its own, apart from the loopback one. More trees join it with the same `--share` address. The lock is named after the address, such as `192.168.1.5-80.lock`, and deleting it stops the share. `mdserve --kill --share` stops every tree on that address. `mdserve --restart --share` moves the trees to a new binary. `mdserve -l` lists shared trees with the rest.

On `0.0.0.0` the printed URL carries the machine's name, since the server answers on every address. `0.0.0.0` claims its port on every address, so it cannot bind a port that any listener holds, the loopback mdserve included. mdserve then fails with `E98` and names the listeners on that port. Share on one address, or give `0.0.0.0` another port.

## What a visitor sees

A shared tree mounts at its base name, eg `http://192.168.1.69/notes/`. The URL does not reveal where the tree lives on the machine.

The server serves only what other users on the machine could read. A file needs the others-read bit, and every directory from the tree's root down to it needs others-read and others-execute. Links out of the tree follow the `.mdserve-follow` rule as on loopback. Everything else in the tree is served, dot files and `.git` included, so share a directory that holds only what you mean to share.

A refused entry shows as refused, without the reason, since the reason names paths outside the tree. The index at `/` lists the shared trees by name. It shows no lock paths, no other addresses and no buttons. No page has a Delete lock or Kill server button, so no visitor can stop the server.

The server answers to any `Host` header, since visitors use whatever name reaches the machine. The Content-Security-Policy forbids every script but live reload's.

## Nobody can connect

Every share start prints a note on stderr after the URL, with the capture that tells where a connection dies:

```
note: other machines connect to 192.168.1.69:80; a firewall here may drop them unseen.
      Check with: sudo tcpdump -ni wlp0s20f3 tcp port 80
```

mdserve cannot find out by itself. A firewall drops the connection before any socket sees it, reading the ruleset needs root, and a test from this machine goes through loopback and passes.

Run the capture and load the URL on the other machine:

- Nothing arrives: the request never reaches this machine. The other machine is on another network, a guest network or mobile data, or the router keeps wifi clients apart
- SYNs arrive and none is answered: a firewall here drops them. `sudo nft list ruleset` shows it; look for `policy drop` on an input chain with no accept for the port
- SYNs are answered with RST: nothing listens there. Check the address in the printed URL

To open the port to the LAN until the firewall reloads:

```
sudo nft insert rule inet <table> input ip saddr 192.168.1.0/24 tcp dport 80 accept
```

`<table>` is the table that drops, such as `filter` or `laptop`.

## Secret paths

`--secret` mounts the tree under a random 128-bit token instead of its name, eg `http://192.168.1.5/lz7sjx3g2cvx54ev7ndbzf7i7q/`, and leaves it off the index. Only those holding the link reach it. `--secret=token` uses the given token, of letters, digits, `-` and `_`.

The token also guards against DNS rebinding. Without it, a hostile web page could read a share through the browser of a visitor on the same network.

The token lies in the tree's lockfile and in the private registry. `--restart` keeps it, and passes it to the new server on the command line, where other local users can see it in `ps`.
