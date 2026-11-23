#!/usr/bin/env python3
"""PostToolUse on Edit/Write: for an edited *.go file run gofmt -l (check only) and go vet on its package."""
import json, os, subprocess, sys

d = json.load(sys.stdin)
path = d.get("tool_input", {}).get("file_path", "")
if not path.endswith(".go") or not os.path.isfile(path):
    sys.exit(0)

root = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=os.path.dirname(path),
                      capture_output=True, text=True).stdout.strip() or d.get("cwd", ".")
problems = []
fmt = subprocess.run(["gofmt", "-l", path], capture_output=True, text=True)
if fmt.stdout.strip() or fmt.returncode:
    problems.append(f"gofmt -l flags {path} (run gofmt -s -w on it):\n{fmt.stdout}{fmt.stderr}")
pkg = "./" + os.path.relpath(os.path.dirname(path), root)
vet = subprocess.run(["go", "vet", pkg], cwd=root, capture_output=True, text=True, timeout=300)
if vet.returncode:
    problems.append(f"go vet {pkg} failed:\n{vet.stdout}{vet.stderr}")
if problems:
    print("\n".join(problems), file=sys.stderr)
    sys.exit(2)
