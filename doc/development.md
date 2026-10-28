# Development

To try a change without installing it, run `make run`. It builds `build/www` and the syntax tree helper and serves this directory on port 8080 with `--debug`, beside an installed server on port 80.

A later run replaces the server on 8080 with the new build, serving the same trees, and open pages go on under it. So edit and `make run` again.

It opens the browser only when no page of this directory was open on the old server, and says which happened. A hidden tab holds no connection, so it does not count and gets a second tab. `www --kill -a 127.31.N.1:8080` stops it.

## The working name

The program's name lives in the file `WORKING_NAME`, since www is a working name until a final one is chosen. The binary, its paths, environment variables, journal tag and URLs, the Makefile and the Debian package all take it from there. A test fails on any other copy outside prose and `go.mod`.

## Usage check

The help text is written in docopt notation. `make test` checks it with docopt-ng, run through `uv`, and skips the check where `uv` is missing.

## Fonts

`make fonts` cuts the embedded fonts from STIX Two Text 2.13 and Iosevka 34.9.1, pinned by SHA-256 in `assets/fonts/subset.py`, and writes `assets/fonts.css`. It needs the network and `uv`. Its output is committed, so a build needs neither.
