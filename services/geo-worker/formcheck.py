"""Check form definitions with the Go validator from packages/form-schema.

The worker image builds that validator as the formcheck command (see the
Dockerfile), so converted forms meet exactly the rules and messages that the
core API and the web app apply. There is no Python copy of the rules.
"""

import json
import os
import subprocess

FORMCHECK = os.environ.get("FIMRA_FORMCHECK", "formcheck")


def check_many(forms):
    """Check several forms in one call. Returns one (problems, columns) per form."""
    if not forms:
        return []
    try:
        done = subprocess.run([FORMCHECK], input=json.dumps(forms, ensure_ascii=False).encode(),
                              capture_output=True, timeout=60, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise RuntimeError(f"the form validator could not run: {exc}") from exc
    if done.returncode != 0:
        raise RuntimeError(f"the form validator failed: {done.stderr.decode(errors='replace').strip()}")
    results = json.loads(done.stdout)
    return [(r["problems"], r.get("columns")) for r in results]


def check(form):
    """The problems with one form, and its columns when it has none."""
    return check_many([form])[0]
