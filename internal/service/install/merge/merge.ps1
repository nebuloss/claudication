# merge.py and merge.awk for Windows, where PowerShell is the one thing every
# machine has. The same rules; the tests on the Go side check the scripts
# that carry this, not this file on its own.

function ConvertFrom-ClaudicationJsonc([string]$s) {
  $out = New-Object Text.StringBuilder
  $i = 0; $n = $s.Length
  while ($i -lt $n) {
    $c = $s[$i]
    if ($c -eq '"') {
      $j = $i + 1
      while ($j -lt $n -and $s[$j] -ne '"') { if ($s[$j] -eq '\') { $j += 2 } else { $j++ } }
      [void]$out.Append($s.Substring($i, [Math]::Min($j + 1, $n) - $i)); $i = $j + 1
    } elseif ($c -eq '/' -and $i + 1 -lt $n -and $s[$i + 1] -eq '/') {
      $j = $s.IndexOf("`n", $i); if ($j -lt 0) { $i = $n } else { $i = $j }
    } elseif ($c -eq '/' -and $i + 1 -lt $n -and $s[$i + 1] -eq '*') {
      $j = $s.IndexOf('*/', $i + 2); if ($j -lt 0) { $i = $n } else { $i = $j + 2 }
    } elseif ($c -eq ',') {
      $j = $i + 1
      while ($j -lt $n -and " `t`r`n".Contains([string]$s[$j])) { $j++ }
      if ($j -ge $n -or -not '}]'.Contains([string]$s[$j])) { [void]$out.Append($c) }
      $i++
    } else { [void]$out.Append($c); $i++ }
  }
  $out.ToString()
}

# Returns $true when merged, $false when the file is not a JSON object and was
# left alone.
function Merge-ClaudicationJson([string]$path, [string]$new, [string[]]$owned) {
  try {
    $text = (ConvertFrom-ClaudicationJsonc ([IO.File]::ReadAllText($path))).Trim()
    if ($text -eq '') { $cur = [pscustomobject]@{} } else { $cur = $text | ConvertFrom-Json }
  } catch { return $false }
  if (-not ($cur -is [pscustomobject])) { return $false }
  $want = (ConvertFrom-ClaudicationJsonc $new) | ConvertFrom-Json

  foreach ($p in $owned) {
    $keys = $p.Split('.')
    $v = $want; $found = $true
    foreach ($k in $keys) {
      if ($v -is [pscustomobject] -and ($v.PSObject.Properties.Name -contains $k)) { $v = $v.$k } else { $found = $false; break }
    }
    if (-not $found) { continue }
    $d = $cur
    for ($i = 0; $i -lt $keys.Count - 1; $i++) {
      $k = $keys[$i]
      if (-not (($d.PSObject.Properties.Name -contains $k) -and ($d.$k -is [pscustomobject]))) {
        $d | Add-Member -Force -NotePropertyName $k -NotePropertyValue ([pscustomobject]@{})
      }
      $d = $d.$k
    }
    $d | Add-Member -Force -NotePropertyName $keys[-1] -NotePropertyValue $v
  }
  foreach ($prop in $want.PSObject.Properties) {
    if (-not ($cur.PSObject.Properties.Name -contains $prop.Name)) {
      $cur | Add-Member -NotePropertyName $prop.Name -NotePropertyValue $prop.Value
    }
  }
  $json = ConvertTo-Json -InputObject $cur -Depth 64
  [IO.File]::WriteAllText($path, $json + "`n", (New-Object Text.UTF8Encoding $false))
  $true
}

function Merge-ClaudicationToml([string]$path, [string]$new) {
  $mark = "# >>> claudication: written by the gateway's install command, rewritten on each run"
  $done = '# <<< claudication'
  $isHeader = '^[ \t]*\['
  $isKey = '^[ \t]*[A-Za-z0-9_-]+[ \t]*='
  $headerOf = { param($l) ($l -replace '^[ \t]*\[+[ \t]*', '') -replace '[ \t]*\]+.*$', '' }
  $keyOf = { param($l) ($l -replace '^[ \t]*', '') -replace '[ \t]*=.*$', '' }

  $keys = @{}; $tables = @(); $head = @(); $body = @(); $top = $true
  foreach ($l in ($new -split "`r?`n")) {
    if ($l -match $isHeader) { $top = $false }
    if ($top) {
      if ($l -match $isKey) { $keys[(& $keyOf $l)] = $true; $head += $l }
    } else {
      if ($l -match $isHeader) { $tables += (& $headerOf $l) }
      $body += $l
    }
  }
  while ($body.Count -gt 0 -and $body[-1].Trim() -eq '') { $body = $body[0..($body.Count - 2)] }
  $owned = { param($h) foreach ($t in $tables) { if ($h -eq $t -or $h.StartsWith("$t.")) { return $true } }; $false }

  $out = New-Object Collections.Generic.List[string]
  $out.Add($mark); foreach ($l in $head) { $out.Add($l) }; $out.Add($done)
  $blanks = 0; $first = $true; $section = ''; $skipping = $false; $marked = $false
  foreach ($l in ([IO.File]::ReadAllText($path) -split "`r?`n")) {
    if ($l.StartsWith('# >>> claudication')) { $marked = $true; continue }
    if ($l.StartsWith('# <<< claudication')) { $marked = $false; continue }
    if ($marked) { continue }
    if ($l -match $isHeader) {
      $section = & $headerOf $l
      $skipping = & $owned $section
      if ($skipping) { continue }
    } elseif ($skipping) { continue
    } elseif ($section -eq '' -and $l -match $isKey -and $keys.ContainsKey((& $keyOf $l))) { continue }
    if ($l.Trim() -eq '') { $blanks++; continue }
    if ($first -and $blanks -eq 0) { $blanks = 1 }
    $first = $false
    for (; $blanks -gt 0; $blanks--) { $out.Add('') }
    $out.Add($l)
  }
  if ($body.Count -gt 0) {
    $out.Add(''); $out.Add($mark); foreach ($l in $body) { $out.Add($l) }; $out.Add($done)
  }
  [IO.File]::WriteAllText($path, ($out -join "`n") + "`n", (New-Object Text.UTF8Encoding $false))
}
