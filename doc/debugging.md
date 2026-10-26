# Debugging

## Bug reports

A small, faint bug button sits at the foot of every page. A click files the page as a numbered bug, and the button says which, eg `bug 7 filed`. Text selected on the page goes in as the note. A field then opens beside the button for a comment: Enter adds it, Escape or clicking away leaves the bug without one.

Each report lands in `~/.local/state/www/bugs/`, or below `$XDG_STATE_HOME` when set:

- `7.kv`: the report as keyval: the time, the page's URL, the note, the window's size and scroll as `view`, the browser as `ua`, the build, and each comment as `comment.0`, `comment.1` and on
- `7.html`: the page as it stood at the click, script changes included, with its stylesheet and no script, so it opens as you saw it
- `next`: the number the next report gets. Numbers only grow, so a bug deleted once fixed never gives its number away

Each report and comment is also one line in the log. Find them with `journalctl --user -t www -g '^bug '`:

```
bug 7: http://127.31.1.1/x/nowhere.md note="the coin is cut off" view=1280x800+0 ua="Mozilla/5.0 ..." build=1aa282c snapshot=/home/t/.local/state/www/bugs/7.html
bug 7 comment: "after a restart"
```

`--no-bug` hides the button and refuses reports. A share never shows it, so no visitor writes files on your machine.

## Build stamp

With `--debug`, each page says when its binary was built and how many seconds ago, the commit it was built from and how long the page took to render, eg `built 12:49, 108 s ago, from 1aa282c+modified, rendered in 3.3 ms`. It sits small and faint above the header, so a glance tells which build served the page. `+modified` marks a build from uncommitted code. www's script counts the seconds up while the page is open. Shares leave the stamp out.
