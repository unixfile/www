"""Cut the embedded web fonts from their upstream releases.

Downloads go to build/fonts/ and are checked against their SHA-256. Each
face becomes two WOFF2 files in assets/fonts/: a core that every page needs
and an extension the browser fetches only for a page with its characters.
assets/fonts.css gets the @font-face rules, with each file's unicode-range
read from the file itself. Needs fontTools with brotli, which make fonts
brings through uv.
"""

import hashlib
import os
import sys
import urllib.request
import zipfile

USAGE = """Usage:
  subset.py [-C DIR]
  subset.py -h | --help
  subset.py -V | --version

Options:
  -h --help     Show this screen.
  -V --version  Show the version.
  -C DIR        Change to DIR first, the repo root [default: .].
"""

VERSION = "1"

STIX = "https://raw.githubusercontent.com/stipub/stixfonts/v2.13b171/fonts/static_otf/"
IOSEVKA = "https://github.com/be5invis/Iosevka/releases/download/v34.9.1/PkgTTF-Unhinted-Iosevka-34.9.1.zip"

SOURCES = {
    "STIXTwoText-Regular.otf": (STIX + "STIXTwoText-Regular.otf", "c4864ca6ec071c2d31d0d8309001faa1ee3517fffb53a31a405a697b71f52ca1"),
    "STIXTwoText-Italic.otf": (STIX + "STIXTwoText-Italic.otf", "cd64481fd10b69469074a3a8172fb5a22cb4116941d8aceafb8c77756b563df8"),
    "STIXTwoText-Bold.otf": (STIX + "STIXTwoText-Bold.otf", "7ef76c666a6704f76ed3fa27bcdda55b36e558b5c2c93b49b03d854db96bdeb5"),
    "STIXTwoText-BoldItalic.otf": (STIX + "STIXTwoText-BoldItalic.otf", "90e88f2ac79d6c00a5ce2d8446bfdf9457aed6afc1159cde33262979d82c8586"),
    "iosevka.zip": (IOSEVKA, "46ab28e2948dc83b7a3e5e8792daf7e0528c76410bc5a6471cbd06339b34ad5f"),
}

# The page stacks name the installed font first and these families second,
# so a machine with the font never fetches a file.
FACES = [
    ("STIX Two Text web", "normal", 400, "STIXTwoText-Regular.otf", "STIXTwoText-Regular"),
    ("STIX Two Text web", "italic", 400, "STIXTwoText-Italic.otf", "STIXTwoText-Italic"),
    ("STIX Two Text web", "normal", 700, "STIXTwoText-Bold.otf", "STIXTwoText-Bold"),
    ("STIX Two Text web", "italic", 700, "STIXTwoText-BoldItalic.otf", "STIXTwoText-BoldItalic"),
    ("Iosevka web", "normal", 400, "iosevka.zip:Iosevka-Regular.ttf", "Iosevka-Regular"),
    ("Iosevka web", "italic", 400, "iosevka.zip:Iosevka-Italic.ttf", "Iosevka-Italic"),
    ("Iosevka web", "normal", 700, "iosevka.zip:Iosevka-Bold.ttf", "Iosevka-Bold"),
    ("Iosevka web", "italic", 700, "iosevka.zip:Iosevka-BoldItalic.ttf", "Iosevka-BoldItalic"),
]


def ranges(spec):
    out = set()
    for part in spec.split():
        lo, _, hi = part.partition("-")
        out.update(range(int(lo, 16), int(hi or lo, 16) + 1))
    return out


# The core is Latin with its punctuation, box drawing for directory trees
# in code blocks, and the signs the program's own pages use: folds, the
# copy button's marks and the link to a repo's forge.
CORE = ranges("""
    0000-00FF 0131 0152-0153 02BB-02BC 02C6 02DA 02DC 0304 0308 0329
    2000-206F 20AC 2122 2190-2193 2212 2215 FEFF FFFD
    2500-257F 25B8 25BE 2713 2717 2934 29C9
""")

# Iosevka covers far more than a reader of code needs, about 8 MB a face:
# its extension keeps European scripts and the symbol blocks code and
# terminal output use. STIX Two's extension is the whole rest of the font.
IOSEVKA_EXT = ranges("""
    0100-052F 1E00-1EFF 2070-209F 20A0-20CF 2100-218F 2190-21FF
    2200-23FF 2400-243F 2460-24FF 2500-25FF 2600-27BF 27C0-2BFF
""")


def usage(code):
    print(USAGE, end="", file=sys.stderr if code else sys.stdout)
    sys.exit(code)


def fail(code, msg):
    print(f"E{code}: {msg}", file=sys.stderr)
    sys.exit(code)


def fetch(name):
    url, want = SOURCES[name]
    p = os.path.join("build/fonts", name)
    if not os.path.exists(p):
        os.makedirs("build/fonts", exist_ok=True)
        print(f"fetching {url}", file=sys.stderr)
        urllib.request.urlretrieve(url, p + ".part")
        os.rename(p + ".part", p)
    with open(p, "rb") as f:
        got = hashlib.sha256(f.read()).hexdigest()
    if got != want:
        fail(74, f"{p}: sha256 {got}, want {want}")
    return p


def source(spec):
    name, _, member = spec.partition(":")
    p = fetch(name)
    if not member:
        return p
    out = os.path.join("build/fonts", member)
    if not os.path.exists(out):
        with zipfile.ZipFile(p) as z, open(out, "wb") as f:
            f.write(z.read(member))
    return out


# span writes cps as a unicode-range. An extension's range may bridge a gap
# of up to gap code points the font lacks, which keeps the rule short; a
# page with such a character fetches the file once and falls back. It never
# bridges a code point in keep, the ones the core asks for: two faces of one
# family must not claim the same character, and a sign of the program's
# own that the font lacks, eg Iosevka's missing ⤴, must not fetch the
# extension on every page.
def span(cps, keep=set(), gap=0):
    out, cps = [], sorted(cps)
    lo = prev = cps[0]
    for c in cps[1:] + [None]:
        if c is not None and c - prev <= gap + 1 and not keep.intersection(range(prev + 1, c)):
            prev = c
            continue
        out.append(f"U+{lo:X}" if lo == prev else f"U+{lo:X}-{prev:X}")
        if c is not None:
            lo = prev = c
    return ", ".join(out)


def cut(src, out, cps, mono):
    from fontTools import subset
    from fontTools.ttLib import TTFont

    opts = subset.Options()
    opts.flavor = "woff2"
    # Mono text sets font-variant-ligatures: none, so ligatures are dead
    # weight there.
    if mono:
        opts.layout_features = [f for f in opts.layout_features if f not in ("liga", "clig", "calt", "dlig")]
    # The source's timestamp, not the time of the cut, so a rerun makes
    # the same bytes.
    font = TTFont(src, recalcTimestamp=False)
    have = set(font.getBestCmap())
    s = subset.Subsetter(opts)
    s.populate(unicodes=cps & have)
    s.subset(font)
    font.flavor = "woff2"
    font.save(out)
    return set(TTFont(out).getBestCmap())


def main(argv):
    root = "."
    args = list(argv)
    while args:
        a = args.pop(0)
        if a in ("-h", "--help"):
            usage(0)
        elif a in ("-V", "--version"):
            print(VERSION)
            return
        elif a == "-C" and args:
            root = args.pop(0)
        elif a.startswith("-C") and len(a) > 2:
            root = a[2:]
        else:
            print(f"E22: unknown argument: {a}", file=sys.stderr)
            usage(22)
    os.chdir(root)
    css = ["/* Made by assets/fonts/subset.py; edit that, not this. */\n"]
    for family, style, weight, spec, base in FACES:
        src = source(spec)
        mono = family.startswith("Iosevka")
        core = cut(src, f"assets/fonts/{base}.woff2", CORE, mono)
        ext = cut(src, f"assets/fonts/{base}-ext.woff2", (IOSEVKA_EXT if mono else ranges("0000-10FFFF")) - core, mono)
        for name, rng in ((base, span(core)), (base + "-ext", span(ext, CORE, 64))):
            css.append(
                "@font-face {\n"
                f'  font-family: "{family}";\n'
                f"  font-style: {style};\n"
                f"  font-weight: {weight};\n"
                "  font-display: swap;\n"
                f'  src: url("fonts/{name}.woff2") format("woff2");\n'
                f"  unicode-range: {rng};\n"
                "}\n"
            )
    with open("assets/fonts.css", "w") as f:
        f.write("".join(css))


if __name__ == "__main__":
    main(sys.argv[1:])
