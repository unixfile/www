"""What every test file shares: the running servers."""

import shutil
import tempfile
from pathlib import Path

import pytest

import harness


@pytest.fixture(scope="session")
def servers():
    """One server of each form, started when a case first asks for it."""
    running = {}
    homes = []

    def get(form):
        if form not in running:
            home = Path(tempfile.mkdtemp(prefix="blackbox-"))
            homes.append(home)
            try:
                running[form] = harness.start(form, home)
            except OSError as err:
                pytest.skip(f"{form}: {err}")
        return running[form]

    yield get
    for server in running.values():
        server.stop()
    for home in homes:
        shutil.rmtree(home, ignore_errors=True)
