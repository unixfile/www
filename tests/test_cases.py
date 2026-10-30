"""Each file in cases/ names the decision it checks and the pages that hold it."""

import re
import shutil
import tempfile
import tomllib
from pathlib import Path

import pytest

import harness

CASES = Path(__file__).parent / "cases"


def requests():
    for file in sorted(CASES.glob("*.toml")):
        case = tomllib.loads(file.read_text())
        for req in case["request"]:
            for form in req.get("servers", case["servers"]):
                yield pytest.param(form, req, id=f"{file.stem}-{form}-{req['path']}")


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


def visible(html):
    """The page without its script and style, whose words a reader never sees."""
    return re.sub(r"<(script|style)\b.*?</\1>", "", html, flags=re.S | re.I)


@pytest.mark.parametrize("form, req", requests())
def test_page(servers, form, req):
    page = servers(form).get(req["path"])
    text = visible(page.text)
    assert page.status_code == req["status"], text
    for want in req.get("contains", []):
        assert want in text
    for ban in req.get("absent", []):
        assert ban.lower() not in text.lower()
