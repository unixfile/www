# Online repos

The server on your default address also listens on the address after it, 127.31.N.2, on the same port. There a URL names a repo on a forge by its host and path, eg `http://127.31.1.2/github.com/junegunn/fzf/README.md`. The first visit fetches the repo into a cache, and from then on www serves it as it serves a checkout: docs, code, listings, search and history. Home and the web never collide, since each has an address of its own.

The index at `/` on the default address links the web address. The web address's own `/` lists the repos in the cache, each with its size and when it was last read, and how much of the disk limit they fill.

## The first visit

A repo not yet cached opens a page that says "reading". It names each step as www takes it: the git command, the URL and where the files go, with git's progress below and its time. When the repo is ready the page turns into it on its own. For most repos that takes one to three seconds; git's own repo takes about 20.

www first finds where the repo ends in the path. It asks `git ls-remote` on every prefix at once, with and without `.git`, and takes the longest that answers; the rest of the path lies inside the repo. GitHub, Bitbucket and Codeberg keep every repo at owner/name, so there it asks nothing and clones at once.

A URL that ends in `.git` redirects to the one without, so a repo has one URL.

When another website links to a repo not yet cached, the browser marks the request as cross-site, and www asks before fetching anything. A URL typed, bookmarked or followed from a www page fetches at once. So no site can make your browser fill the disk.

## The cache

The cache lives in `~/.cache/www/`, or `$XDG_CACHE_HOME/www/`, one directory per repo named by its host and path. Each holds a bare clone without file contents, in `.git`, and one worktree of the commit served, such as `.wt-b412a450538e`, which the link `.tree` names. The worktree's checkout fetches every file of that commit in one request. Older file versions arrive when a page needs them: a commit's diff fetches the blobs it shows, which takes about half a second on GitHub.

`--disk size` sets the cache's limit, 2G by default. When a fetch takes it over, www deletes the repos read longest ago until it fits. A repo larger than the limit on its own goes too, and its page says so.

Delete the directory any time; www fetches what it needs again.

## Staying current

A visit to a repo fetched over five minutes ago serves the cached commit at once and fetches in the background. When the branch moved, www builds the new commit's worktree beside the old one and swaps the link, so no page sees a half-updated tree, and open pages reload onto the new commit. The header of every page in the repo names the commit shown and when it was fetched. When a fetch fails, the page keeps the commit it has and says the fetch failed.

A fetch takes the branches and tags. It leaves out GitHub's pull request refs, which would fill a large repo's cache with commits nobody asked for.

## Listings

A path that names no repo lists the repos below it, eg `/github.com/junegunn` or `/git.suckless.org`. git cannot list an account, so here alone www asks something else, in this order:

- the forge's API: GitHub, GitLab, Bitbucket, and Forgejo or Gitea on any other host, such as Codeberg
- a web frontend's index: gitweb's project index, or the front page of cgit or stagit
- the cache, which holds only what was read here

The listing names its source and the sources that failed first, so a partial listing does not pass for a whole one. It is kept for an hour, since GitHub allows 60 calls an hour without a token.

## What git may do

Every git call against a forge runs without your credentials, without your git config and without prompts or hooks, and only over HTTPS. A forge that asks for a login gets none, so only public repos open. Loopback is open to every account on the machine, and a private repo fetched here would be open to them too.

Elsewhere git still may not fetch at all. git 2.44 and later honour `GIT_NO_LAZY_FETCH`; before it, `protocol.allow=never` alone holds that line.

A share never serves the web address.
