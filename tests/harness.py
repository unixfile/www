"""Run the binary named by TEST_BIN in a throwaway home and ask it for pages."""

import os
import signal
import socket
import subprocess
import time
from pathlib import Path

import httpx

GIT_DATE = "2026-01-02T03:04:05Z"

# Every repo the seeded cache holds. The host never resolves, so no case
# reaches a network.
SEEDED = {
    "forge.invalid/acct/r": {
        "README.md": "# r\n",
        "long.go": "package long\n" + "// a line\n" * 300,
    }
}


def binary():
    path = os.environ.get("TEST_BIN")
    if not path:
        raise RuntimeError("TEST_BIN must name the binary; make blackbox sets it")
    return Path(path).resolve()


def environment(home):
    """The whole environment of a run: nothing leaks in from the caller's."""
    return {
        "PATH": os.environ["PATH"],
        "HOME": str(home),
        "XDG_CACHE_HOME": str(home / "cache"),
        "XDG_CONFIG_HOME": str(home / "config"),
        "XDG_STATE_HOME": str(home / "state"),
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_CONFIG_NOSYSTEM": "1",
        "TZ": "UTC",
    }


def git(cwd, *args):
    env = {
        "PATH": os.environ["PATH"],
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_AUTHOR_NAME": "a",
        "GIT_AUTHOR_EMAIL": "a@forge.invalid",
        "GIT_AUTHOR_DATE": GIT_DATE,
        "GIT_COMMITTER_NAME": "a",
        "GIT_COMMITTER_EMAIL": "a@forge.invalid",
        "GIT_COMMITTER_DATE": GIT_DATE,
    }
    done = subprocess.run(["git", *args], cwd=cwd, env=env, check=True, capture_output=True, text=True)
    return done.stdout.strip()


def seed(cache, key, files):
    """Lay out one repo as the binary leaves it after a clone.

    A public server fetches only over https from the open internet, so a
    case with no network cannot have it clone. This is the one place the
    suite knows the cache's layout.
    """
    source = cache.parent / "sources" / key
    source.mkdir(parents=True)
    git(source, "init", "-q", "-b", "main")
    for name, text in files.items():
        (source / name).parent.mkdir(parents=True, exist_ok=True)
        (source / name).write_text(text)
    git(source, "add", ".")
    git(source, "commit", "-q", "-m", "first")
    head = git(source, "rev-parse", "HEAD")
    repo = cache / key
    repo.mkdir(parents=True)
    git(repo, "clone", "-q", "--bare", str(source), ".git")
    name = ".wt-" + head[:12]
    git(repo / ".git", "worktree", "add", "-q", "--detach", "--", str(repo / name), head)
    (repo / ".tree").symlink_to(name)
    (repo / ".fetched").touch()
    (repo / ".used").touch()


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Server:
    """One running binary. origin is where its repos answer."""

    def __init__(self, home, argv, origin, listens):
        self.origin = origin
        self.log = home / "stderr"
        self.proc = subprocess.Popen(
            [str(binary()), *argv],
            cwd=home,
            env=environment(home),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=self.log.open("w"),
        )
        self._wait(listens)

    def _wait(self, addr):
        end = time.monotonic() + 10
        while time.monotonic() < end:
            if self.proc.poll() is not None:
                raise RuntimeError(f"exit {self.proc.returncode}: {self.log.read_text()}")
            try:
                socket.create_connection(addr, timeout=0.2).close()
                return
            except OSError:
                time.sleep(0.02)
        raise TimeoutError(f"nothing listens on {addr}: {self.log.read_text()}")

    def get(self, path):
        """The page at path once any read it set going has ended."""
        end = time.monotonic() + 30
        while True:
            page = httpx.get(self.origin + path, trust_env=False, timeout=30)
            if page.status_code != 202 or time.monotonic() > end:
                return page
            time.sleep(0.1)

    def stop(self):
        self.proc.send_signal(signal.SIGTERM)
        try:
            self.proc.wait(10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()


# public: the flag the README names for the open web.
# default: a reader's server, as wwwvm's unit runs it, whose online repos
# answer on the .2 beside the user's default address.
def start(form, home):
    """Start the binary in one of the forms a server runs in."""
    cache = home / "cache" / binary().name
    for key, files in SEEDED.items():
        seed(cache, key, files)
    port = free_port()
    if form == "public":
        return Server(home, ["--public", "--disk=1G", "-a", f"127.0.0.1:{port}"],
                      f"http://127.0.0.1:{port}", ("127.0.0.1", port))
    if form == "default":
        n = os.getuid() - 1000
        if not 0 <= n <= 255:
            raise OSError(f"uid {os.getuid()} has no default address")
        tree = home / "tree"
        tree.mkdir()
        return Server(home, ["--foreground", "--no-open", "--no-bug", "--disk=1G", "-a", f"127.31.{n}.1:{port}", str(tree)],
                      f"http://127.31.{n}.2:{port}", (f"127.31.{n}.2", port))
    raise ValueError(form)
