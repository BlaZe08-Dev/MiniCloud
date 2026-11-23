# Security finding template

Source: `docs/source/MINICLOUD_SECURITY_INSTRUCTIONS.md` section 7. Copy into the
handoff or review, or into `docs/security/findings.md` (next SEC number, via the task
that owns that file). Report before fixing (break-before-fix). Add an evidence label
(AGENTS.md section 2): executed | designed, not executed | confirmed by code reading | inferred.

### Finding
A concise description of the issue.

### Severity
Critical, High, Medium, Low, or Informational.

### Affected Component
The specific MiniCloud component or feature.

### Preconditions
What must be true for the issue to occur.

### Reproduction Steps
Exact, safe steps to reproduce the failure.

### Evidence
Logs, test output, observed behavior, or a minimal proof of concept (real output, pasted verbatim). Evidence label:

### Impact
What an attacker, user, or system failure could cause.

### Root Cause
Why the implementation behaves incorrectly.

### Recommended Fix
A concrete remediation strategy, including trade-offs.

### Regression Test
A test that should prevent the issue from returning (name only if verified; otherwise "(planned test)").

### Status
Confirmed, Unconfirmed, Fixed, or Retested.
