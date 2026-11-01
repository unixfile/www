"""Each file in cases/ names the decision it checks and the pages that hold it."""

import re
import tomllib
from pathlib import Path

import pytest

CASES = Path(__file__).parent / "cases"


def requests():
    for file in sorted(CASES.glob("*.toml")):
        case = tomllib.loads(file.read_text())
        for req in case["request"]:
            for form in req.get("servers", case["servers"]):
                yield pytest.param(form, req, id=f"{file.stem}-{form}-{req['path']}")


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
