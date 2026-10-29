# Pages

URLs start with the served directory's path relative to home, eg `http://127.31.1.1:8080/scratch/github.com/unixfile/webstack/index.md`, so a URL names its tree. A directory outside home uses its absolute path.

Below the prefix, URLs mirror the file tree, so relative links in the docs work unchanged. A tree inside another served tree answers for its own paths.

A `.md` file renders as a page, a directory as a listing of its names, a text file as a highlighted code page with linkable lines, and anything else raw. Add `?raw` to any URL for the file itself.

## Missing pages

An absolute link such as `/x.md` points outside the prefix and gives 404. Its page names the trees nearest it, or every tree when none is near.

A path the tree lacks gives 404 and names the whole URL; the digits turn like a coin before they stop. Below, the page says where git saw the path go: the commit that deleted or moved it, with a link to its last version. It also names the files with names close to it in the nearest directory that exists.

## Links

Bare URLs in a doc become links.

A relative link in a doc whose target is missing gets a dotted red underline and names the missing path on hover. A link that climbs out of the tree is not checked, since another tree may answer there.

## Docs

Docs follow CommonMark with GitHub's extensions, rendered by goldmark: tables, strikethrough, task lists, bare URLs as links, footnotes and definition lists.

Lines join into a paragraph, and a blank line starts the next. A line that starts with a dash begins a list even right after text. Straight quotes and dashes turn typographic.

A quote whose first line is only `[!NOTE]`, `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]` or `[!CAUTION]` becomes an alert. The marker gives way to a small mono label that names its kind, and the alert keeps full strength where a plain quote fades. A marker with text after it on the same line stays a plain quote.

Wiki links work as relative links do. `[[name]]` opens `name.md` in the doc's directory and `[[sub/name]]` opens `sub/name.md`. A name with an extension, eg `[[main.go]]`, keeps it. `[[name|label]]` shows the label and `[[name#heading]]` opens at the heading.

A fenced block marked `dot` or `graphviz` becomes a diagram, drawn by Graphviz's `dot` when it is installed and kept by the hash of its source. Without `dot`, or when `dot` rejects the graph, the block shows as code under a line that says why. `dot` may not read image files the graph names.

Every gap between blocks is the same, whether the block is a paragraph, list, table or code. Headings sit close to their text.

## Linked from

Below a doc, linked from names each doc in the tree that links to it, by any relative spelling.

The list arrives after the page, since the server walks the tree for it: a few milliseconds in a repo, about 250 ms across all of `~/scratch`, 77,000 files. The server keeps each doc's links and reads a doc again only when its size or time changes. It reads a new tree's links in the background when the tree joins.

## Recent pages

The browser remembers the pages you open; the server keeps no such list. A search shows the best five that match its words first, under recent. They come from the tree you are in, so one repo's reading never shows in another's. A version counts as its tree.

Each visit counts and recent visits weigh more, as in zoxide. A reload is no visit.

Each address is an origin of its own, so each visitor to a share sees only the pages that visitor opened. Online repos keep a list apart from the trees.

## Live reload

Open pages reload when what they show changes.

A doc takes its new version in place, without a reload. Each block that changed fills with the highlight colour and fades over three seconds, so the eye finds the change after a save.

A code page reloads when its file is written, a listing when a name in it changes, any page in a repo when its HEAD moves, and the index when a tree joins or leaves. A page whose file or tree is gone waits two seconds for it to return, then reloads to say so.

A page outlives its server. After three seconds without it, a line over the page says the server is gone and since when, and the page keeps trying each second. When a new server answers, eg after `make run` or `--restart`, the page takes its version from it and a line says new server, fading over three seconds. A restart takes about 60 ms, so its new server answers before the gone line shows.

Each page holds a server-sent events stream while its tab is visible. A hidden tab lets go, since a browser opens at most six connections to one host, and checks again when shown.

The server watches the directory of each open page and its repo's git directory through inotify, since editors save by renaming a new file over the old one. It polls every second where inotify fails.

## Look

The stylesheet is www's own. It began as md2html-default's, whose light palette it keeps, and the page colours sit as variables on `:root`.

A page turns dark when the OS asks for it through `prefers-color-scheme`, with a dark palette and syntax colours of its own. The dark palette is a first draft.

The browser loads the stylesheet once. Its URL names a hash of its content, eg `/_www/style.7b94a08e7731.css`, so it is kept for a year, and a changed stylesheet comes under a new URL.

The header names a tree by its base name and shows the full path on hover. Every tree page's breadcrumbs start at `index`, which links `/`.

The header, listings and www's own messages are mono; documents keep the serif. Mono text shows no ligatures, so `<=` stays two characters in any font.

Every page keeps one width, 58ch of the body font, and keeps space for a scrollbar, so the header holds still from page to page. Code is half the size of the body text, never under 11px, so about 115 columns fit; longer lines scroll.

## Fonts

Pages load no fonts from the network. STIX Two Text, the serif, and Iosevka, the mono, come from the machine when installed. Otherwise they come from subsets built into www, which the browser caches for a year.

Each face has a core file for Latin text and www's own signs, about 25 KB for the serif and 15 KB for the mono, so a plain page fetches about 40 KB of font. A second file per face holds Greek, Cyrillic and symbols, and the browser fetches it only for a page that uses them.

Text shows at once in a fallback font and switches when the file arrives.
