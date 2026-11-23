#!/usr/bin/env python3
"""PreToolUse guard. Enforces AGENTS.md hard rules 2, 8, 9, 10 mechanically.
Exit 2 blocks the call and sends stderr to Claude. Heuristic (regex on the command string):
it can be bypassed by obfuscation and may flag a quoted mention; it is a safety net, not a sandbox."""
import json, re, subprocess, sys

d = json.load(sys.stdin)
tool, ti = d.get("tool_name", ""), d.get("tool_input", {})

def block(msg):
    print(f"BLOCKED by .claude/hooks/guard.py: {msg}", file=sys.stderr)
    sys.exit(2)

if tool in ("Edit", "Write", "NotebookEdit"):
    path = ti.get("file_path", "")
    base = path.rsplit("/", 1)[-1]
    if base == "go.sum":
        block("go.sum is generated; no new dependency without owner approval + ADR (AGENTS.md section 1).")
    if re.fullmatch(r"\.env(\..*)?", base) and base != ".env.example":
        block(".env files hold secrets and are gitignored (AGENTS.md hard rule 9).")
    sys.exit(0)

if tool != "Bash":
    sys.exit(0)

cmd = ti.get("command", "")
rules = [
    (r"\bgit\s+add\s+(?:\S+\s+)*(?:-A|--all|\.)(?:\s|$|;|&|\|)", "stage by explicit path, not -A / . (hard rule 8)"),
    (r"\bgit\s+add\b[^;&|]*\s\.env(?:\s|$)", "never stage .env (hard rule 9)"),
    (r"\bgit\s+push\b[^;&|]*(?:--force\b|--force-with-lease\b|\s-f\b|\s\+\S)", "no force-push (hard rule 2)"),
    (r"\bgit\s+push\b[^;&|]*\b(?:origin\s+)?(?:HEAD:)?(?:refs/heads/)?main\b", "never push main (hard rule 2)"),
    (r"\bgit\s+reset\s+--hard\b", "git reset --hard needs owner approval (hard rule 10)"),
    (r"\bgit\s+branch\s+(?:-\w*D\w*|--delete)\b", "never delete branches (hard rule 2)"),
    (r"\bmake\s+(?:db-down|db-reset-test)\b", "destructive on the shared DB, owner approval needed (hard rule 10)"),
    (r"\bdocker\s+volume\s+(?:rm|prune)\b|\bdocker\s+compose\s+down\b[^;&|]*\s-v\b|\bdocker\s+system\s+prune\b", "deletes volumes, owner approval needed (hard rule 10)"),
]
for pat, why in rules:
    if re.search(pat, cmd):
        block(why)

if re.search(r"\bgit\s+(?:commit|merge|rebase|cherry-pick|revert|am)\b", cmd):
    try:
        br = subprocess.run(["git", "branch", "--show-current"], cwd=d.get("cwd") or None,
                            capture_output=True, text=True, timeout=10).stdout.strip()
    except Exception:
        br = ""
    if br in ("main", "master"):
        block(f"on branch '{br}': never edit main, work on agent/<agent>/<task> (hard rule 2)")
sys.exit(0)
