#!/usr/bin/env python3
"""Stop hook: run `make gate` only if .go files differ from HEAD (modified, staged or untracked)
and that exact state has not already passed. The hash is stored only after a PASS: storing it
after a failure would let an unchanged, still-broken tree through on the next Stop."""
import hashlib, json, os, subprocess, sys

d = json.load(sys.stdin)
if d.get("stop_hook_active"):  # already continued once because of this hook; avoid a loop
    sys.exit(0)
cwd = d.get("cwd") or None

def git(*a):
    return subprocess.run(["git", *a], cwd=cwd, capture_output=True).stdout

if not git("status", "--porcelain", "--", "*.go").strip():
    sys.exit(0)

h = hashlib.sha256(git("diff", "HEAD", "--", "*.go"))
for f in sorted(git("ls-files", "-z", "--others", "--exclude-standard", "--", "*.go").split(b"\0")):
    if f:
        h.update(b"\0" + f + b"\0")
        h.update(open(os.path.join(cwd or ".", f.decode()), "rb").read())
digest = h.hexdigest()
stamp = os.path.join(cwd or ".", ".gate.hash")
try:
    if open(stamp).read().strip() == digest:
        sys.exit(0)  # this exact .go state already passed the gate
except FileNotFoundError:
    pass

r = subprocess.run(["make", "gate"], cwd=cwd, capture_output=True, text=True)
if r.returncode:
    print("make gate FAILED after .go changes (full log: .gate.log). Fix, do not weaken tests.\n"
          + r.stdout[-3000:] + r.stderr[-1000:], file=sys.stderr)
    sys.exit(2)
open(stamp, "w").write(digest + "\n")
