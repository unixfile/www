"""Pages in a real browser, asserting layout and never pixels."""

import shutil

import pytest
from playwright.sync_api import Error, sync_playwright

PAGE = "/forge.invalid/acct/r/long.go"

# A bar's bottom and the line's top may differ by the scroll's rounding.
ROUNDING = 1


@pytest.fixture(scope="module")
def browser():
    """Playwright's Chromium where installed, else the machine's Brave."""
    kept = shutil.which("brave-browser")
    tries = [{}, {"executable_path": kept}] if kept else [{}]
    with sync_playwright() as p:
        for options in tries:
            try:
                # basic: Brave otherwise asks the desktop's wallet for a password.
                b = p.chromium.launch(args=["--password-store=basic", "--use-mock-keychain"], **options)
            except Error as err:
                failure = str(err).splitlines()[0]
                continue
            yield b
            b.close()
            return
    pytest.skip(f"no browser starts: {failure}")


@pytest.fixture
def origin(servers):
    """Where the code page answers, past any reading page a first request gets."""
    server = servers("public")
    server.get(PAGE)
    return server.origin


@pytest.fixture
def page(browser):
    page = browser.new_page(viewport={"width": 1280, "height": 800})
    yield page
    page.close()


def assert_below_bars(page, line):
    got = page.evaluate(
        """(line) => ({
            line: document.getElementById(line).getBoundingClientRect().top,
            search: document.querySelector("div.search").getBoundingClientRect().bottom,
            view: document.querySelector("div.view").getBoundingClientRect().bottom,
            y: scrollY,
        })""",
        line,
    )
    assert got["y"] > 0, got
    assert got["line"] >= max(got["search"], got["view"]) - ROUNDING, got
    assert got["line"] < max(got["search"], got["view"]) + 40, got


# Code lines anchor as #L42. A marked line lands below the search row and
# the view links.
@pytest.mark.parametrize("query", ["", "?scopes"])
@pytest.mark.parametrize("fragment", ["#L100", "#L100-L110"])
def test_load_marks_a_line_below_the_bars(origin, page, query, fragment):
    page.goto(origin + PAGE + query + fragment, wait_until="load")
    assert_below_bars(page, "L100")


def test_click_marks_a_line_below_the_bars(origin, page):
    page.goto(origin + PAGE, wait_until="load")
    page.evaluate("""scrollTo(0, document.getElementById("L200").getBoundingClientRect().top + scrollY - 300)""")
    # The number is the link's ::before, so the link itself has no size to click.
    page.locator("#L200 > a").first.dispatch_event("click")
    page.wait_for_function("""location.hash === "#L200" """)
    assert_below_bars(page, "L200")


# The details view leads each row with size and age. Sizes of different
# widths, here 4 and 3.0K, still leave the names in one column.
def test_details_names_line_up(origin, page):
    page.goto(origin + "/forge.invalid/acct/r/?details", wait_until="load")
    lefts = page.evaluate(
        """[...document.querySelectorAll("ul.listing li:not(.cmds) > a")]
            .map((a) => a.getBoundingClientRect().left)"""
    )
    assert len(lefts) == 2, lefts
    assert max(lefts) - min(lefts) < ROUNDING, lefts
