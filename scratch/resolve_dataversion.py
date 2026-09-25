#!/usr/bin/env python3
"""Resolve the recurring internal/db/db.go dataVersion rebase conflict.

Handles ONLY hunks of this exact shape:

    <<<<<<< HEAD
    const dataVersion = N
    =======
    // (M: comment...)
    // more comment lines
    const dataVersion = M
    >>>>>>> <sha> (msg)

Resolution: renumber incoming comment entries sequentially from N+1 and
set `const dataVersion = N + <number of incoming entries>`. Any other
conflict shape is left untouched and reported.

Prints `map OLD=NEW` lines for each renumbered entry so callers can fix
`CurrentDataVersion()` assertions added by the same pick.
"""
import re
import sys

PATH = "internal/db/db.go"

text = open(PATH).read()

pattern = re.compile(
    r"<<<<<<< HEAD\n"
    r"const dataVersion = (\d+)\n"
    r"=======\n"
    r"((?://.*\n)+?)"
    r"const dataVersion = \d+\n"
    r">>>>>>> [0-9a-f]+.*\n"
)

mappings = []


def resolve(m):
    head_n = int(m.group(1))
    comments = m.group(2)
    out_lines = []
    next_v = head_n + 1
    for line in comments.splitlines(keepends=True):
        mm = re.match(r"^// \((\d+):", line)
        if mm:
            mappings.append((int(mm.group(1)), next_v))
            line = re.sub(r"^// \(\d+:", f"// ({next_v}:", line)
            next_v += 1
        out_lines.append(line)
    out_lines.append(f"const dataVersion = {next_v - 1}\n")
    return "".join(out_lines)


new_text, count = pattern.subn(resolve, text)
open(PATH, "w").write(new_text)

remaining = len(re.findall(r"^<<<<<<< ", new_text, re.M))
print(f"resolved {count} hunk(s); {remaining} conflict marker(s) remain in db.go")
for old, new in mappings:
    print(f"map {old}={new}")
sys.exit(0 if remaining == 0 and count > 0 else 2)
