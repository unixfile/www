# /// script
# requires-python = ">=3.9"
# dependencies = ["docopt-ng==0.9.0"]
# ///
"""Check command lines against a usage text with docopt-ng.

Usage:
  docopt-check.py USAGE < LINES

Reads USAGE, a file of docopt usage text, then one command line per line
on stdin, split as the shell would. Prints E22 for each line docopt
rejects and exits 22 if any was rejected or the usage text is malformed.
"""
import shlex
import sys

import docopt


def main():
    if len(sys.argv) != 2:
        sys.stderr.write("E22: usage: docopt-check.py USAGE < LINES\n")
        return 22
    with open(sys.argv[1]) as f:
        usage = f.read()
    failed = False
    for line in sys.stdin:
        line = line.rstrip("\n")
        try:
            docopt.docopt(usage, shlex.split(line), default_help=False)
        except docopt.DocoptLanguageError as e:
            sys.stderr.write("E22: the usage text is not docopt: %s\n" % e)
            return 22
        except SystemExit:
            sys.stderr.write("E22: docopt rejects: %s\n" % line)
            failed = True
    return 22 if failed else 0


sys.exit(main())
