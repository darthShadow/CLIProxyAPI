#!/opt/homebrew/bin/bash
set -euo pipefail
umask 077
root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
tool="$root/internal/api/handlers/management/usage_archive_css"
assets="$root/internal/api/handlers/management/usage_archive"
build=/tmp/claude-501/cliproxy-migration/usage-css/tailwind
cache=/tmp/claude-501/cliproxy-migration/usage-css/bun-cache
rm -rf "$build"
mkdir -p "$build/css" "$build/templates" "$cache"
cp "$tool/package.json" "$tool/bunfig.toml" "$build/"
cp "$tool/input.css" "$build/css/input.css"
cp "$assets/usage-archive.html" "$assets/fragment.html" "$assets/accounts.html" "$build/templates/"
python3 - "$build/templates/usage-archive.html" <<'PY'
from pathlib import Path
import sys
p=Path(sys.argv[1]);s=p.read_text();a='/* tailwind:begin */';b='/* tailwind:end */'
assert s.count(a)==s.count(b)==1
start,end=s.index(a)+len(a),s.index(b);assert start<end
p.write_text(s[:start]+'\n'+s[end:])
PY
cd "$build"
export BUN_INSTALL_CACHE_DIR="$cache"
if [[ -f "$tool/bun.lock" ]]; then
    cp "$tool/bun.lock" .
    bun install --frozen-lockfile
else
    bun install
    cp bun.lock "$tool/bun.lock"
fi
bun --bun ./node_modules/.bin/tailwindcss -i css/input.css -o - --minify > compiled.css
python3 - "$assets/usage-archive.html" "$build/compiled.css" <<'PY'
from pathlib import Path
import sys
p=Path(sys.argv[1]);s=p.read_text();css=Path(sys.argv[2]).read_text()
a='/* tailwind:begin */';b='/* tailwind:end */'
assert s.count(a)==s.count(b)==1 and 'tailwindcss v4.3.2' in css
start,end=s.index(a)+len(a),s.index(b);assert start<end
p.write_text(s[:start]+'\n'+css.rstrip()+'\n'+s[end:])
print('compiled CSS bytes:',len(css.encode()))
PY
