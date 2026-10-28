# Search and controls

The box under the header searches what is below it as you type, ignoring case. It holds the keyboard, so typing on any page searches, wherever focus is.

## What a search finds

On a listing it searches the directory: names that hold the query first, then lines in text files. The directory's own entries show at full strength, and what lies in its subdirectories follows after a gap, dimmed. A line result opens a code page at its line.

Any other page filters itself, and each match is marked:

- a code page keeps its matching lines with their numbers
- a doc keeps its matching blocks
- a history keeps its matching commits
- a syntax tree keeps the rows whose field, kind or text holds the query, with their folds opened

Enter jumps to the first match in the whole page. Below, after a gap and dimmed, follow the directory's results without the page's own file.

The query stays in the URL as `?q=`, so Back from a result or a reload shows the results again.

Hidden files are searched too, but not the stores machines write, such as `.git`, `.venv` or `.cache`, nor files over the code page limit. A search stops after half a second and says so. A share searches only what it serves.

## Keys

Escape clears the query and brings the page back. Escape on an empty query goes up, eg from a file to its directory or from a commit to its history. It highlights the page it left, so Enter goes back.

Up and Down move a highlight through a listing's entries, or through a search's matches and results, and Enter opens it. A match opens at its place in the whole page. The highlight survives a reload.

Left and Right move the cursor in the box and never navigate. With nothing to walk, Up, Down and Space scroll the page.

Shift+Tab in the box enters the header at the crumb Escape goes to, and Tab goes on from there to raw on a doc and Delete lock. Escape on a focused link or button returns to the box.

On a touch screen the box takes the keyboard only when tapped, since it would raise the on-screen keyboard.

## Copy buttons

Beside the box, ⧉ copies the page's text: what lies below the box, without www's own lines and links or line numbers. A code page copies as its file and a listing as its names. A doc copies its Markdown, and lines a range marks, eg `#L3-L26`, copy alone. What a search hides stays out.

Beside it, ⤴ copies a link to what is selected:

- selected or marked lines on a code page, pinned to the commit when the file is clean, eg `@261024.22/server.go#L10-L12`
- other selected text, as a link that scrolls to it and marks it, `#:~:text=`
- with nothing selected, the page

The link carries the page's own address, so a share's link opens on other machines and a loopback one only here. On the web address it is the forge's own link at the commit shown, eg `https://github.com/junegunn/fzf/blob/<commit>/main.go#L10-L12`, which opens in any browser.

## When controls show

The search row, box and buttons alike, sticks to the top of the window as the page scrolls. All three stay at hand while reading, and a jump to a line stops below the row.

The box, its buttons, raw, the stop button and a code page's views fade while nothing needs them, and keep their space. The search row then lets the text scroll through it. The crumbs, the header's status and the bug button stay.

They show while the page sits at its top, since the gaps they leave there look odd. Once the first line scrolls under the search row, they show only:

- while the query holds a character
- while the mouse moves over the column
- while keyboard focus sits on one of them or a crumb
- when the page scrolls up

Once shown, they fade only 2 s after the last of these ends, never at once.
