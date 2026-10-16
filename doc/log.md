# Log

mdserve logs every action, always. There is no flag and no level to set, because a filter set before a mistake drops the entry that would explain it.

Entries go to the systemd journal, tagged `mdserve`:

```
journalctl --user -t mdserve --since -1h
journalctl --user -t mdserve MDSERVE_ENDPOINT=127.31.1.1-80
journalctl --user -t mdserve -p err
```

Each entry carries the endpoint it concerns in `MDSERVE_ENDPOINT` and its priority in `PRIORITY`: 3 for faults, 6 for the rest. The journal also holds what explains a vanished server, such as a suspend, an OOM kill or a logout.
Where no journal socket exists, entries go to `~/.local/state/mdserve/log` instead, or below `$XDG_STATE_HOME` when set. Its lines hold time, PID, endpoint, priority and the message. The file is mode 600 and rotates once to `log.1` at 1 MB. `mdserve --help` names the place in use.

## What is logged

Every run of `mdserve`, with its arguments, working directory and binary, and its outcome when it fails. A run that joins a server or finds its tree already served says so.
Each server's start with its binary and commit, eg `/usr/bin/mdserve 54cca2a`, and `+modified` for a build from uncommitted code. A stale binary is the first suspect when a page looks wrong.
Each tree served and withdrawn, with the cause: a deleted lock, a Delete lock or Kill server button, or a signal. Then the server's stop and exit.
Stale locks and registry links cleared after a crash, and the SIGTERM that `--kill` and `--restart` send.
Every page request, with method, path, status and time taken. Live reload streams, the stylesheet, fonts and the favicon come with every page and are left out. A shared server adds the visitor's address.
Faults: pandoc and git failures, refused links, inotify falling back to polling, and what net/http reports. A detached server's stderr goes to the same place, so a panic is kept.

A secret share's token never reaches the log. Paths do, and on shares so do visitors' addresses. Your user journal is readable by you and by members of `adm` and `systemd-journal`.
