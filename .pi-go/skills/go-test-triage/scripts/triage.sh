#!/usr/bin/env bash
# Print one line per failing test, plus a package count, and nothing else.
#
# Runs from the directory the agent is working in, not from the skill directory:
# the skill ships the script, the project supplies the code.
set -uo pipefail

pattern="${1:-./...}"

out=$(go test -count=1 "$pattern" 2>&1)
status=$?

packages=$(printf '%s\n' "$out" | grep -cE '^(ok|FAIL|---|\?)' || true)

if [ $status -eq 0 ]; then
  echo "PASS packages=$(printf '%s\n' "$out" | grep -c '^ok' || true)"
  exit 0
fi

printf '%s\n' "$out" | grep -E '^(--- FAIL|FAIL|.*\.go:[0-9]+:)' | head -40
echo "packages_seen=$packages"
exit 1
