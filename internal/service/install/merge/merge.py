# Merges the gateway's settings into a client's JSON or JSONC config.
#   python3 merge.py TARGET NEW OWNED...
# Each OWNED path (dotted) is replaced whole by NEW's value; NEW's other
# top-level keys are added only where TARGET has none. Everything else in
# TARGET is kept. Comments in TARGET are not: JSON has nowhere to keep them,
# and the backup beside it does. Exit 3: TARGET is not a JSON object, and is
# left alone.
import json
import sys


def strip(s):
    """JSONC to JSON: comments and trailing commas out, strings untouched."""
    out, i, n = [], 0, len(s)
    while i < n:
        c = s[i]
        if c == '"':
            j = i + 1
            while j < n and s[j] != '"':
                j += 2 if s[j] == "\\" else 1
            out.append(s[i:j + 1])
            i = j + 1
        elif s.startswith("//", i):
            j = s.find("\n", i)
            i = n if j < 0 else j
        elif s.startswith("/*", i):
            j = s.find("*/", i + 2)
            i = n if j < 0 else j + 2
        elif c == ",":
            j = i + 1
            while j < n and s[j] in " \t\r\n":
                j += 1
            if j >= n or s[j] not in "}]":
                out.append(c)
            i += 1
        else:
            out.append(c)
            i += 1
    return "".join(out)


def main():
    target, new, owned = sys.argv[1], sys.argv[2], sys.argv[3:]
    try:
        with open(target, encoding="utf-8") as f:
            text = strip(f.read()).strip()
        cur = json.loads(text) if text else {}
    except (ValueError, OSError):
        return 3
    if not isinstance(cur, dict):
        return 3
    with open(new, encoding="utf-8") as f:
        want = json.loads(strip(f.read()))

    for path in owned:
        keys = path.split(".")
        v = want
        try:
            for k in keys:
                v = v[k]
        except (KeyError, TypeError):
            continue
        d = cur
        for k in keys[:-1]:
            if not isinstance(d.get(k), dict):
                d[k] = {}
            d = d[k]
        d[keys[-1]] = v
    for k, v in want.items():
        cur.setdefault(k, v)

    with open(target, "w", encoding="utf-8") as f:
        f.write(json.dumps(cur, indent=2, ensure_ascii=False) + "\n")
    return 0


sys.exit(main())
