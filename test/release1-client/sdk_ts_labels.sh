#!/opt/homebrew/bin/bash
# The real Agent SDK sends a main turn and a session title with each custom
# entrypoint. PASS: both labels pass sdk_ts.sh's refusal and identity checks.
# FAIL: either label fails; the second label runs even when the first fails.
set -euo pipefail
FIXTURE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
failed_labels=""
"$BASH" "$FIXTURE_DIR/sdk_ts.sh" shadow-code 18573 || failed_labels="shadow-code"
"$BASH" "$FIXTURE_DIR/sdk_ts.sh" t3-code 18576 || failed_labels="${failed_labels:+$failed_labels }t3-code"
if [ -n "$failed_labels" ]; then
	echo "FAIL: sdk_ts_labels: $failed_labels"
	exit 1
fi
echo "PASS: sdk_ts_labels: shadow-code and t3-code main turns and session titles keep their identity"
