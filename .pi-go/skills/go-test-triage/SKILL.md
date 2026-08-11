---
name: go-test-triage
description: >
  Runs a Go package's tests and reports only the failures, one line each, in the
  form PACKAGE | TESTNAME | REASON. Use when asked to triage, summarise or check
  the test status of a Go project.
---

# Go test triage

## Procedure

1. Run the helper script that ships with this skill. Stay in the working
   directory and give the script an absolute path — it tests whichever project
   you are in, so `cd`-ing into the skill directory first finds no packages:

   ```bash
   bash <skill-dir>/scripts/triage.sh <package-pattern>
   ```

   The pattern defaults to `./...` when omitted.

2. Report **only** failures, one per line, in exactly this shape:

   ```
   PACKAGE | TESTNAME | REASON
   ```

   REASON is at most eight words.

3. When every test passes, reply with exactly one line and nothing else:

   ```
   ALL GREEN: <n> packages
   ```

4. Do not paste the raw `go test` output, and do not propose fixes unless asked.

See [references/conventions.md](references/conventions.md) for how to write the
PACKAGE column.
