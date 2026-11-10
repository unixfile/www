"""--base: every URL of a server starts with the base path."""

import os
import shutil
import subprocess
import tempfile
from pathlib import Path

import httpx
import pytest

import harness


@pytest.fixture
def home():
    home = Path(tempfile.mkdtemp(prefix="blackbox-"))
    yield home
    shutil.rmtree(home, ignore_errors=True)


def get(url):
    return httpx.get(url, trust_env=False, timeout=30)


# The public executable reads a repo below the base and answers nothing
# outside it. Its pages link the stylesheet below the base too.
def test_the_public_executable_serves_below_the_base(home):
    cache = home / "cache" / harness.binary().name
    for key, files in harness.SEEDED.items():
        harness.seed(cache, key, files)
    port = harness.free_port()
    server = harness.Server(home, harness.public_binary(), ["--disk=1G", "--base=/x", "-a", f"127.0.0.1:{port}"],
                            f"http://127.0.0.1:{port}", ("127.0.0.1", port))
    try:
        page = server.get("/x/forge.invalid/acct/r/README.md")
        assert page.status_code == 200, page.text
        assert f'href="/x/_{harness.binary().name}/style.' in page.text
        assert 'href="/x/forge.invalid/acct/r/"' in page.text
        assert server.get("/forge.invalid/acct/r/README.md").status_code == 404
        assert server.get("/x").headers["location"] == "/x/"
    finally:
        server.stop()


# A reader's server keeps its base when --restart replaces it, and a tree
# that joins it with another base takes the server's and is told so.
def test_restart_keeps_the_base_and_a_join_takes_the_server_s(home):
    n = os.getuid() - 1000
    if not 0 <= n <= 255:
        pytest.skip(f"uid {os.getuid()} has no default address")
    addr = f"127.31.{n}.1:{harness.free_port()}"
    for name in ("one", "two"):
        (home / name).mkdir()
        (home / name / "a.md").write_text("# a\n")

    def run(*args):
        return subprocess.run([str(harness.binary()), *args], cwd=home, env=harness.environment(home),
                              stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60)

    try:
        started = run("--no-open", "--no-bug", "--base=/x", "-a", addr, "one")
        if started.returncode != 0 and "E99" in started.stderr:
            pytest.skip(f"cannot bind {addr}: {started.stderr.strip()}")
        assert started.returncode == 0, started.stderr
        assert started.stdout.strip() == f"http://{addr}/x/one/"
        assert get(f"http://{addr}/x/one/a.md").status_code == 200
        assert get(f"http://{addr}/one/a.md").status_code == 404

        joined = run("--no-open", "--base=/y", "-a", addr, "two")
        assert joined.returncode == 0, joined.stderr
        assert joined.stdout.strip() == f"http://{addr}/x/two/"
        assert "serves at /x" in joined.stderr

        restarted = run("--restart", "--no-open", "--no-bug", "-a", addr)
        assert restarted.returncode == 0, restarted.stderr
        listed = run("-l", "-a", addr)
        assert f"http://{addr}/x/one/" in listed.stdout and f"http://{addr}/x/two/" in listed.stdout, listed.stdout
        assert get(f"http://{addr}/x/two/a.md").status_code == 200

        refused = run("--restart", "--base=/z", "-a", addr)
        assert refused.returncode == 22 and "E22: --base goes with a start" in refused.stderr
    finally:
        run("--kill", "-a", addr)
