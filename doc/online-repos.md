# Online repos

The server on your default address also listens on the address after it, 127.31.N.2, on the same port. There a URL names a repo on a forge by its host and path, eg `http://127.31.1.2/github.com/junegunn/fzf/README.md`. The first visit fetches the repo into a cache, and from then on www serves it as it serves a checkout: docs, code, listings, search and history. Home and the web never collide, since each has an address of its own.

The index at `/` on the default address links the web address. The web address's own `/` lists the repos in the cache, each with its size and when it was last read, and how much of the disk limit they fill, and links back to the trees on the default address.

## Ways in

The index at `/` names the web address, but a reader seldom sees the index, so two ways in lie where a reader already is.

A link in a doc to a repo on GitHub, GitLab, Codeberg or Bitbucket, or to a file or directory in one, is followed by a small www, raised like a footnote mark. It opens the same repo or file on the web address, and its title names the repo. The forge's own link stays as it was. A link to a forge page that is not a file, eg an issue or an account, gets no mark. A file link names a branch or commit, but the web address serves the commit it fetched, so the mark opens the file as it is there. The copy button leaves the mark out.

A forge URL typed or pasted into the search box, with or without `https://`, adds a result above the others, under "online". Enter opens it, since it comes first. A URL to any other host, eg `https://git.suckless.org/dwm`, gets no result there; paste it after the web address instead, scheme and all, eg `http://127.31.1.2/https://git.suckless.org/dwm`. The web address drops the scheme, and turns a forge's view of a file, eg `/blob/master/README.md`, into the file.

Both appear wherever a server serves the web address: on its default address and on the web address itself. A share offers neither.

## The first visit

A repo not yet cached opens a page that says "reading". It names each step as www takes it: the git command, the URL and where the files go, with git's progress below and its time. When the repo is ready the page turns into it on its own. For most repos that takes one to three seconds; git's own repo takes about 20.

www first finds where the repo ends in the path. It asks `git ls-remote` on every prefix at once, with and without `.git`, and takes the longest that answers; the rest of the path lies inside the repo. GitHub, Bitbucket and Codeberg keep every repo at owner/name, so there it asks nothing and clones at once.

A URL that ends in `.git` redirects to the one without, so a repo has one URL.

When another website links to a repo not yet cached, the browser marks the request as cross-site, and www asks before fetching anything. The page's read link keeps the link's fragment, eg the lines `#L10-L12` marks, and the page never reloads on its own, so nothing is fetched until you follow it. A URL typed, bookmarked or followed from a www page fetches at once. So no site can make your browser fill the disk.

## The cache

The cache lives in `~/.cache/www/`, or `$XDG_CACHE_HOME/www/`, one directory per repo named by its host and path. Each holds a bare clone without file contents, in `.git`, and one worktree of the commit served, such as `.wt-b412a450538e`, which the link `.tree` names. The worktree's checkout fetches every file of that commit in one request. Older file versions arrive when a page needs them: a commit's diff fetches the blobs it shows, which takes about half a second on GitHub. A version fetches a directory's files in one request, the first time its listing or one of its files opens, so its first page takes about the same.

`--disk=SIZE` sets the cache's limit, 2G by default. When a fetch takes it over, www deletes the repos read longest ago until it fits. A repo larger than the limit on its own goes too, and its page says so.

Delete the directory any time; www fetches what it needs again.

## Staying current

A visit to a repo fetched over five minutes ago serves the cached commit at once and fetches in the background. When the branch moved, www builds the new commit's worktree beside the old one and swaps the link, so no page sees a half-updated tree, and open pages reload onto the new commit. The header of every page in the repo names the commit shown and when it was fetched, eg `@ 52502e6 · fetched 23:40`, as a version's crumbs name a commit; the hash links the page in that commit's version, which stays put after the next fetch. When a fetch fails, the page keeps the commit it has and says the fetch failed.

A fetch takes the branches and tags. It leaves out GitHub's pull request refs, which would fill a large repo's cache with commits nobody asked for.

## Versions and links

A repo here has versions as a local tree has: `@` and a commit below the repo's address, eg `/github.com/junegunn/fzf@b412a45/main.go`, shows the repo as it was then, and its pages, history and share button link them. The clone holds every commit and tree, so a version's listings come at once; its files are fetched from the forge as a page first needs them.

The share button here copies the forge's own link at the commit the page shows, eg `https://github.com/junegunn/fzf/blob/<commit>/main.go#L10-L12`, since a link to this address opens only on this machine. Lines take the forge's form: `#L10-L12` on GitHub and Codeberg, `#L10-12` on GitLab and `#lines-10:12` on Bitbucket. A host www knows no link form for gets the page's own address.

## Listings

A path that names no repo lists the repos below it, eg `/github.com/junegunn` or `/git.suckless.org`. git cannot list an account, so here alone www asks something else, in this order:

- the forge's API: GitHub, GitLab, Bitbucket, and Forgejo or Gitea on any other host, such as Codeberg
- a web frontend's index: gitweb's project index, or the front page of cgit or stagit
- the cache, which holds only what was read here

The listing names its source and the sources that failed first, so a partial listing does not pass for a whole one. It is kept for an hour, since GitHub allows 60 calls an hour without a token.

## On the open web

`www --public --disk=SIZE -a ADDR` serves online repos to anyone who reaches the address, eg through Caddy in front of it. It serves no tree from the machine and no other address, and it runs in the foreground for a service manager. `--share=HOST` binds a public address directly instead.

A visitor learns nothing of the machine or of what others read. `/` says only how to read a repo. A listing that no forge or web frontend gives is a 404, never the cache. Pages write the cache's paths from `cache/`, git's errors included. No page has a stop or bug button, nothing opens a browser, and any Host header is answered, since nothing local is there to protect.

`--disk` is required, since any visitor can make www clone. The limits aim at a bot that makes www clone without end, not at a reader. A repo already in the cache opens at any time. A visit to an uncached path starts a read: a probe, a clone or a listing. One address runs one read at a time and starts at most 20 within an hour; over that it gets a 429 with `Retry-After`, and the log names the address. An IPv6 address counts by its /64. Behind a proxy on loopback, eg Caddy, the visitor is the last address in `X-Forwarded-For`. At most four clones run at once, and a fifth waits on its reading page.

One leak remains: a repo already in the cache opens at once, and one that is not shows the reading page first.

## What git may do

Every git call against a forge runs without your credentials, without your git config and without prompts or hooks, and only over HTTPS. A forge that asks for a login gets none, so only public repos open. Loopback is open to every account on the machine, and a private repo fetched here would be open to them too.

Elsewhere git still may not fetch at all. git 2.44 and later honour `GIT_NO_LAZY_FETCH`; before it, `protocol.allow=never` alone holds that line.

A share never serves the web address.
