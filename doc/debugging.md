# Debugging

With `--debug`, each page says when its binary was built and how many seconds ago, the commit it was built from and how long the page took to render, eg `built 12:49, 108 s ago, from 1aa282c+modified, rendered in 3.3 ms`. It sits small and faint above the header, so a glance tells which build served the page. `+modified` marks a build from uncommitted code. www's script counts the seconds up while the page is open. Shares leave the stamp out.

A bug button ends the stamp. It logs the page as a bug: one line in the log, and a snapshot of the page as it stands, script changes included, in `~/.local/state/www/bugs/`. Text selected on the page goes in as the note. Find the reports with `journalctl --user -t www -g '^bug:'`:

```
bug: http://127.31.1.1/x/nowhere.md note="the coin is cut off" view=1280x800+0 ua="Mozilla/5.0 ..." build=1aa282c snapshot=/home/t/.local/state/www/bugs/20261026-142821.746.html
```

`view` is the window's size and how far it was scrolled. The snapshot holds its stylesheet and no script, so it opens as the reader saw it.
