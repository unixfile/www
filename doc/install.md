# Install

www needs git.
`make install` links `build/www` into `~/.local/bin`.
`make deb` builds a Debian package in `build/`, such as `www_0~git20261015223930.20f02fd_amd64.deb`, versioned by the last commit's UTC time and hash. It depends on git. For another architecture, pass `ARCH`, eg `make deb ARCH=arm64`.
`make install-deb` builds the package while sudo asks for the password, then installs it with `sudo apt install`, which pulls in git. Debian's needrestart lists the services that use old libraries after the install instead of asking which to restart; none of them is www. It first removes the `~/.local/bin` link from `make install`, which would otherwise shadow the package's binary. Run it again to replace an installed build, even of the same commit. `make uninstall-deb` removes the package.

The package grants `/usr/bin/www` one capability, `cap_net_bind_service`, so it can bind port 80 while running as the user. `getcap /usr/bin/www` shows it. With it www serves on port 80 and URLs carry no port; without it, as after `make install`, the port is 8080. www also takes port 80 where the machine lets every user bind low ports.

Add `*.*.*.*-*.lock` and `*.*.*.*-*.LOCK` to `~/.config/git/ignore` so locks stay out of `git status`.
