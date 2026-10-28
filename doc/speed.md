# Speed

www is fast. A page from a running server takes one to three milliseconds and a server starts in about 40 ms. A test in `make test` fails any page that takes over 5 ms.

## Git's answers

A server keeps git's answers, so most pages run no git at all. It keeps which repo each directory lies in, the histories pages show and the commits they open. Commits never change and are kept by their full hash. `--cache` sets the memory for all of it.

The server reads HEAD from the repo's files, and a history is kept under the commit HEAD names. A new commit misses the cache, so nothing shown goes stale. A `.git` that appears or goes makes the server ask git again.

The cache holds only what someone has visited, with one exception. When a tree joins, the server asks git in the background for what its listing and its README show, since those open first. The log says how long that took.

## Slow histories

In a large repo git can take a second for a history, because it reads every commit back to the ones that touched the path.

When it takes over 100 ms, the history page says so and names `git commit-graph write --reachable --changed-paths`. That builds git's index of the paths each commit changed and makes such histories several times faster. www never writes to the repo itself.

## Benchmarks

`make bench` times what a reader waits for: a search across a generated tree of 4,000 files and 24 MB, and a code page's first view per language. Each language is timed three ways: lexing alone, the whole page, and the page with scope boxes.

`BENCH` picks benchmarks by pattern and `COUNT` repeats them, eg `make bench BENCH=Search COUNT=10` for benchstat.

Python, JavaScript and shell are timed on argparse.py, jquery.js and bash_completion from Debian's packages, and skipped where those are not installed.

Warm pages have no benchmark, since `TestPageBudget` in `make test` holds them under 5 ms.
