# Merges the gateway's settings into a client's TOML config.
#   awk -v new=NEW -f merge.awk TARGET > MERGED
# What the gateway owns is what NEW defines: its top-level keys and its
# tables, sub-tables included. Those are taken out of TARGET wherever they
# are, along with any block an earlier run marked, and NEW's are put back in
# two marked blocks — the keys first, since TOML reads a key after a table
# header as part of that table, and the tables last. Everything else in
# TARGET is kept as it was, comments included.

function header(line,    h) {
  h = line
  sub(/^[ \t]*\[+[ \t]*/, "", h)
  sub(/[ \t]*\]+.*$/, "", h)
  return h
}

function key(line,    k) {
  k = line
  sub(/^[ \t]*/, "", k)
  sub(/[ \t]*=.*$/, "", k)
  return k
}

function owned(h,    t) {
  for (t in tables) if (h == t || index(h, t ".") == 1) return 1
  return 0
}

# Blank lines are held until something follows them, so the blocks taken out
# and put back on each run do not leave a growing gap behind.
function emit(line) {
  if (line ~ /^[ \t]*$/) { blanks++; return }
  if (first && blanks == 0) blanks = 1
  first = 0
  for (; blanks > 0; blanks--) print ""
  print line
}

BEGIN {
  mark = "# >>> claudication: written by the gateway's install command, rewritten on each run"
  done = "# <<< claudication"
  top = 1
  while ((getline line < new) > 0) {
    if (line ~ /^[ \t]*\[/) top = 0
    if (top) {
      if (line ~ /^[ \t]*[A-Za-z0-9_-]+[ \t]*=/) { keys[key(line)] = 1; head = head line "\n" }
    } else {
      if (line ~ /^[ \t]*\[/) tables[header(line)] = 1
      body = body line "\n"
    }
  }
  close(new)
  printf "%s\n%s%s\n", mark, head, done
  first = 1
  section = ""
}

/^# >>> claudication/ { marked = 1; next }
/^# <<< claudication/ { marked = 0; next }
marked { next }

/^[ \t]*\[/ {
  section = header($0)
  skipping = owned(section)
  if (!skipping) emit($0)
  next
}
skipping { next }
section == "" && /^[ \t]*[A-Za-z0-9_-]+[ \t]*=/ && (key($0) in keys) { next }
{ emit($0) }

END {
  if (body != "") {
    blanks = 1
    first = 0
    emit(mark)
    printf "%s%s\n", body, done
  }
}
