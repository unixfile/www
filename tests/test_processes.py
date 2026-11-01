"""Cases about processes and their locks, run against real ones."""

import shutil
import subprocess
import tempfile
from pathlib import Path

import pytest

import harness

# With --mount-proc the binary reads the new namespace from its own /proc.
# Without it /proc still belongs to the old namespace and cannot name this
# process at all, which must count as another namespace too.
NAMESPACES = {
    "own-proc": ["unshare", "--user", "--map-current-user", "--pid", "--fork", "--mount-proc"],
    "old-proc": ["unshare", "--user", "--map-current-user", "--pid", "--fork"],
}


@pytest.fixture
def served():
    """A running server, its throwaway home and its tree's lock."""
    home = Path(tempfile.mkdtemp(prefix="blackbox-"))
    try:
        server = harness.start("default", home)
    except OSError as err:
        shutil.rmtree(home, ignore_errors=True)
        pytest.skip(str(err))
    yield server, home, next((home / "tree").glob("*.lock"))
    server.stop()
    shutil.rmtree(home, ignore_errors=True)


def inside(prefix, home, *args):
    """Run the binary in a new PID namespace, or skip where none can be made."""
    if subprocess.run([*prefix, "true"], capture_output=True).returncode != 0:
        pytest.skip("unshare is refused here, eg in a sandbox; run make blackbox outside it")
    return subprocess.run([*prefix, str(harness.binary()), *args], cwd=home,
                          env=harness.environment(home), capture_output=True, text=True, timeout=30)


@pytest.mark.parametrize("namespace", NAMESPACES)
def test_list_keeps_a_lock_from_another_pid_namespace(served, namespace):
    server, home, lock = served
    done = inside(NAMESPACES[namespace], home, "-l")
    assert done.returncode == 0, done.stderr
    assert str(home / "tree") in done.stdout
    assert lock.exists()
    assert server.proc.poll() is None


@pytest.mark.parametrize("namespace", NAMESPACES)
def test_kill_refuses_a_server_in_another_pid_namespace(served, namespace):
    server, home, lock = served
    done = inside(NAMESPACES[namespace], home, "--kill")
    assert done.returncode == 1, done.stderr
    assert "E1: " in done.stderr and "another PID namespace" in done.stderr
    assert lock.exists()
    assert server.proc.poll() is None
