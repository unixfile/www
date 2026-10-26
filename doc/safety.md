# Safety

www refuses to serve the home directory and every directory above it, such as `/home` and `/`. A tree exposes everything below it, and home holds `~/.ssh`, `~/.gnupg` and every token file.
Symlinks inside the tree are followed, absolute ones included. A link that leads out of the tree is followed only into a directory that holds a file named `.www-follow`, or below one, and only when other users could read the target. A file needs the others-read bit and a directory others-read and others-execute, checked from the marker's directory down. So nobody reads through www what `cat` would not show them.
A store of private files linked into repos, such as lord's `~/.local/share/lord`, opens with one marker in its root. Secrets kept there at mode 600 stay refused. Opening a refused link shows the link, its target and the reason, and the listing marks it refused with the same on hover.
A loopback server answers only to loopback addresses, `localhost` and names that `/etc/hosts` maps to loopback. Its Content-Security-Policy forbids every script but www's own, which reloads pages, marks changed blocks, walks listings by arrow and opens history folds, and which it allows by hash. Raw HTML in a doc passes through, so a README in a cloned repo could otherwise run code on this origin.
www runs no code a tree supplies. It reads files and runs git with every option that would start a program named in the repo's config turned off: the pager, textconv, external diff, signature checks, fsmonitor and fetching.

The addresses keep users out of each other's way, not out of each other's trees. Loopback is open to every local account, so on a machine shared with other users, they can reach your server. Serve nothing there you would not show them.
