#!/bin/sh
# Runs govulncheck on the shipping binaries (./cmd/...) and fails when they reach a vulnerability, except the
# advisories .github/govulncheck-skip lists: known, mitigated, and waiting for a release that fixes them. It fails too
# when a listed advisory no longer turns up, so the list never outlives the fixes.
#
# Usage: scripts/govulncheck.sh (needs govulncheck and jq)
set -eu
cd "$(dirname "$0")/.."

skip_file=.github/govulncheck-skip
report=$(mktemp)
trap 'rm -f "$report"' EXIT

govulncheck -format json ./cmd/... >"$report"

# The advisories the binaries call into: the findings whose trace starts at a function, the ones govulncheck's text
# output reports. A finding at module or package level only means the code is present, never called.
found=$(jq -r 'select(.finding.trace[0].function != null) | .finding.osv' "$report" | sort -u)
skipped=$(sed -e 's/#.*//' "$skip_file" | tr -s ' \t' '\n' | grep -v '^$' | sort -u || true)

listed() { printf '%s\n' "$2" | grep -qx "$1"; }

status=0
for id in $found; do
	summary=$(jq -r --arg id "$id" 'select(.osv.id == $id) | .osv.summary' "$report" | head -n 1)
	if listed "$id" "$skipped"; then
		echo "Skipped, listed in $skip_file: $id, $summary"
	else
		echo "Reachable from the binaries: $id, $summary (https://pkg.go.dev/vuln/$id)"
		status=1
	fi
done
for id in $skipped; do
	if ! listed "$id" "$found"; then
		echo "No longer found: $id; remove it from $skip_file"
		status=1
	fi
done
if [ "$status" -eq 0 ]; then
	echo "No reachable vulnerability outside $skip_file."
fi
exit "$status"
