# History

Add `?log` to a path for its git history: the commits that touched it, newest first, one line each with its stardate and subject. A directory's history covers everything below it. A history lists the 500 newest commits.

A listing whose directory has commits opens its history from its first entry, `<(git log)`. A page outside a repo shows git's complaint.

## A history's lines

The stardate leads each line, so the stardates line up, and the version menu orders its rows the same way. A commit with no message shows its day alone. Hover shows the hash and the exact time.

A line opens to the commit's full message and its diff, limited to the path. The diff loads when the line first opens. Bodies wrapped by hand join into paragraphs; a list item or an indented line keeps its own line.

A row is one target: a click anywhere on it opens or closes the fold. Inside, the full hash links the commit's own page and browse links its version.

## Stardates

Each commit shows its stardate, eg 261024.4, the fourth commit of 24 Oct 2026 on the repo's main line. The main line is the first-parent line of origin's main or master, else of the branch origin's HEAD names, eg origin/trunk, else of the local main or master.

A commit a merge brought in is named after the merge, eg 261024.4.2.

A commit on the local main or master that origin's line has not reached yet shows its day and x, eg 261026.x. Its number comes with the push, so a rebase before it renames nothing. Several commits can share a day and x, so a history or the timeline adds each one's short hash, eg 261026.x 6376dd4. Its version's address uses the hash.

A commit on no main line, eg on another branch, shows its short hash.

## Where the reader is

A doc or code page with commits opens with a line that says where the reader is: current when the page matches its last commit, changed when it has edits since. Hover names the commit by stardate and subject.

www compares git's id for the file with the one HEAD keeps, which takes microseconds, and asks `git status` only when they differ.

## The timeline

A ▸ leads the line, the browser's own sign that it opens, and turns to ▾ while open. A click anywhere on the line opens the timeline: a menu of every version of the page, newest first, then the history. `?timeline` serves it as a page.

The version shown is marked. The newest row leads to the current page and says current. When the page has uncommitted changes, a row of its own above it says so.

The menu loads when the pointer first passes over the line, so it opens full. Up and Down walk it, Enter opens a version, and Escape or a click outside closes it. On a share, where there are no versions, its rows open the commits.

## Commit pages

A commit's hash in a history's fold, or commit at the right end of a version's top line, opens the commit on a page of its own. It shows the stardate and full hash as plain text, to select and give to git, then the message and the diff. The diff is limited to the file or directory the history was for, with a link to all the files the commit changed in the tree.

Added lines sit on a green tint and removed ones on a red. The diff stops at 1 MiB.

From the terminal, `www --commit=261024.4` prints a commit's full hash, stardate and subject as keyval.

## Semantic diffs

The diff shows at once. The semantic diff follows a moment later, above the diff: each file's declarations the commit added, removed, changed, renamed, moved or only reformatted, eg `changed tree.serveSearch` or `moved Away from x.go`.

Each name opens a page of that declaration alone. It shows what the commit did to it and its diff from its old self, which a rename or a move between files follows. It links to its lines before and after where the tree has versions.

Within the diff, the tokens that changed in a changed line sit on a stronger tint, so a renamed argument marks the name alone. A run of lines that shares under half its tokens with the lines it replaced is left unmarked.

Declarations come from the syntax helper, `make syntax`, and the token marks from the lexers. Without the helper the page says so and keeps the marks.

A first view of a commit that touches 12 files took about 190 ms. The server keeps the result by commit, so a later view takes a few milliseconds and shows it with the page.

## Git's safety

www runs `git log`, `git show` and `git rev-parse` in the directory of what the page shows, after links resolve.

A cloned repo's `.git/config` can name programs for git to run. So www turns off external diff, textconv, signature checks and fetches, and drops `GIT_` variables from its environment. git's own `safe.directory` check refuses repos that other users own.

No history page shows authors, on a share or on loopback.

## On shares

A shared server shows history and diffs as loopback does, with four differences.

History appears only when other users could read the repo's git directory, its `HEAD`, `objects` and `refs`. Nobody reads through www what git run as another user would not show them.

Each message loses its trailer block, which carries emails and session links in lines such as `Signed-off-by:` and `Claude-Session:`. As in git, the last paragraph is a trailer block when a quarter of its lines are `Key: value` lines, so a cherry-pick note or a wrapped trailer does not keep it.

A page in a repo the share keeps private never reloads on a commit. Diffs leave out files whose names begin with a dot, as listings do.

A share never shows git's errors, which name paths on the machine, and says only `No git history here.`

## Old versions of a tree

Each commit in a history has a browse link, inside its fold, to the page as it was in that commit.

`@` and a commit's hash on a tree's name serve the tree as of that commit, eg `/scratch/www@5430ac3/` for its root or `/scratch/www@5430ac3/doc/history.md` for one doc. The version qualifies the tree, so it sits on the name, not below it as an entry would. A tree served at `/` has no name, so its versions sit below it, eg `/@5430ac3/`.

The address below the tree, eg `/scratch/www/@5430ac3/`, is the old form and redirects. A tree that holds an entry named like a version, eg a directory `@cafe`, serves that entry at `/scratch/www/@cafe/`, and the old form does not redirect there.

A stardate works the same, eg `www@261024.4/`, and a hash of a commit with a stardate redirects to it. A day alone, eg `www@261024/`, names no commit and says so, though its six digits would also read as a hash. Any length of hash from four characters up works and redirects to git's short form.

Docs, code pages, listings and search work there as on the tree itself, read from git rather than the disk. Relative links stay in the same version. www's script still runs there, so keys, the search box and line marks work as on the tree.

The top line names the commit that last changed the page as of that version, stardate first as in histories, and opens the same timeline with that version marked. At the line's right end, commit opens the commit's page, and current leads to the page as it is, as does the timeline's first row. The breadcrumbs and the tab title carry the hash, eg `www @ 5430ac3`.

A version never changes, so it has no live reload, no Delete lock button and no path to copy, and its docs list no backlinks.

Shares and the web address have no versions. A page whose history lies in a nested repo, or behind a link, lists its commits but no versions, since the tree's versions do not hold it.
