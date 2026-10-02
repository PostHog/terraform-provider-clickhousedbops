"""Run with python3 .github/scripts/test-release-version.py."""

import os
from pathlib import Path
import subprocess
import tempfile
import textwrap

workflow = Path(__file__).resolve().parents[1] / "workflows/posthog-release.yaml"
step = workflow.read_text().split("      - name: Choose next patch version\n", 1)[1]
script = textwrap.dedent(step.split("        run: |\n", 1)[1].split("\n      - name:", 1)[0])
mocks = """
gh() { printf '%s\\n' "$TEST_PREVIOUS"; return "$TEST_API_STATUS"; }
git() { printf '%s\\n' "$*" >> "$TEST_TAG_LOG"; return "$TEST_TAG_STATUS"; }
"""

for previous, api_status, tag_status, expected in [
    ("v0.5.1", 0, 0, "v0.5.2"),
    ("v1.2.9", 0, 0, "v1.2.10"),
    ("v0.0.0", 0, 0, "v0.0.1"),
    ("v1.02.3", 0, 0, None),
    ("v0.5.2-rc.1", 0, 0, None),
    ("", 0, 0, None),
    ("v0.5.1", 1, 0, None),
    ("v0.5.1", 0, 1, None),
]:
    with tempfile.TemporaryDirectory() as directory:
        output = Path(directory) / "output"
        tag_log = Path(directory) / "tag"
        env = dict(os.environ, TEST_PREVIOUS=previous, TEST_API_STATUS=str(api_status),
                   TEST_TAG_STATUS=str(tag_status), TEST_TAG_LOG=str(tag_log),
                   GH_REPO="PostHog/terraform-provider-clickhousedbops",
                   GITHUB_SHA="merged-commit", GITHUB_ENV=str(output))
        result = subprocess.run(["bash", "-e", "-o", "pipefail", "-c", mocks + script],
                                env=env, capture_output=True, text=True)
        if expected is None:
            assert result.returncode != 0, (previous, result.stdout, result.stderr)
            assert not output.exists(), previous
            if tag_status == 0:
                assert not tag_log.exists(), previous
        else:
            assert result.returncode == 0, result.stderr
            assert output.read_text() == f"GORELEASER_PREVIOUS_TAG={previous}\n"
            assert tag_log.read_text() == f"tag {expected} merged-commit\npush origin {expected}\n"

print("Release version checks passed (including API and tag failures)")
