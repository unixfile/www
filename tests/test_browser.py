"""Pages in a real browser, asserting layout and never pixels."""

import shutil

import pytest

import harness
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



# The history keeps to the right end of a listing's first row. A public
# server offers no history for a seeded repo, since its forge does not
# resolve, so this reads a repo in the default server's tree.
def test_history_ends_the_first_row(servers, page):
    server = servers("default")
    repo = server.home / "tree" / "hist"
    repo.mkdir()
    harness.git(repo, "init", "-q")
    (repo / "a.md").write_text("a\n")
    harness.git(repo, "add", ".")
    harness.git(repo, "commit", "-q", "-m", "first")
    page.goto(server.origin.replace(".2:", ".1:") + "/tree/hist/", wait_until="load")
    got = page.evaluate(
        """({
            history: document.querySelector("ul.listing li.cmds > a.history").getBoundingClientRect().right,
            row: document.querySelector("ul.listing li.cmds").getBoundingClientRect().right,
        })"""
    )
    assert abs(got["history"] - got["row"]) < ROUNDING, got


def overflow(page, selector):
    """How far each scroll box's content runs past its width, in px."""
    return page.evaluate(
        """(selector) => [...document.querySelectorAll(selector)].map((e) => e.scrollWidth - e.clientWidth)""",
        selector,
    )


# A code block in a document holds 99 columns in the measure: a line of 99
# does not scroll and a line of 100 does.
def test_a_document_s_code_fits_99_columns(servers, page):
    server = servers("public")
    server.get("/forge.invalid/acct/cols/cols.md")
    page.goto(server.origin + "/forge.invalid/acct/cols/cols.md", wait_until="load")
    page.evaluate("document.fonts.ready")
    fits, over = overflow(page, "main pre")
    assert fits == 0, (fits, over)
    assert over > 0, (fits, over)


# A code page holds 104 columns: 99 of code after a gutter of five cells,
# which a four-digit line number and a space fill.
def test_a_code_page_fits_99_columns_and_four_digits(servers, page):
    server = servers("public")
    server.get("/forge.invalid/acct/cols/fits.go")
    page.goto(server.origin + "/forge.invalid/acct/cols/fits.go", wait_until="load")
    page.evaluate("document.fonts.ready")
    assert overflow(page, "div.code > div.sourceCode") == [0]
    got = page.evaluate(
        """(() => {
            const line = document.getElementById("L1000");
            const text = document.createRange();
            text.selectNodeContents(line);
            const box = text.getBoundingClientRect();
            return {
                gutter: box.left - document.querySelector("div.code > div.sourceCode").getBoundingClientRect().left,
                cell: box.width / line.textContent.length,
                columns: line.textContent.length,
            };
        })()"""
    )
    assert got["columns"] == 99, got
    assert abs(got["gutter"] - 5 * got["cell"]) < ROUNDING, got
    page.goto(server.origin + "/forge.invalid/acct/cols/over.go", wait_until="load")
    page.evaluate("document.fonts.ready")
    assert overflow(page, "div.code > div.sourceCode")[0] > 0


# A diff holds 99 columns, its sign included: an added line of 98 does not
# scroll and one of 99 does. A commit page gives each file its own diff.
def test_a_diff_fits_99_columns(servers, page):
    server = servers("default")
    repo = server.home / "tree" / "diffcols"
    repo.mkdir()
    harness.git(repo, "init", "-q")
    (repo / "a-fits.txt").write_text("x" * 98 + "\n")
    (repo / "b-over.txt").write_text("x" * 99 + "\n")
    harness.git(repo, "add", ".")
    harness.git(repo, "commit", "-q", "-m", "first")
    head = harness.git(repo, "rev-parse", "HEAD")
    page.goto(server.origin.replace(".2:", ".1:") + "/tree/diffcols/?commit=" + head, wait_until="load")
    page.evaluate("document.fonts.ready")
    fits, over = overflow(page, "pre.diff")
    assert fits == 0, (fits, over)
    assert over > 0, (fits, over)
