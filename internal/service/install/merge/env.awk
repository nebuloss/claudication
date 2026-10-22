# Rewrites an env file of `export NAME=value` lines from NEW, keeping the
# lines of the variables in KEEP as they are in CURRENT: an update keeping
# the key the client was installed with, since it never carries one.
#   awk -v keep="NAME..." -f env.awk CURRENT NEW > MERGED
# Every other line is NEW's, in NEW's order; a kept variable takes the place
# of its line in NEW, and one CURRENT does not set is left out (the script
# refuses such a file before it gets here).

function name(line) {
  if (match(line, /^export [A-Za-z_][A-Za-z0-9_]*=/)) return substr(line, 8, RLENGTH - 8)
  return ""
}

BEGIN {
  n = split(keep, names, " ")
  for (i = 1; i <= n; i++) want[names[i]] = 1
}

FNR == NR { v = name($0); if (v != "" && v in want) old[v] = $0; next }

{
  v = name($0)
  if (v != "" && v in want) {
    if (v in old) print old[v]
    next
  }
  print
}
