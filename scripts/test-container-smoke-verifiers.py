import json
import os
import signal
import subprocess
import tempfile
import unittest
from pathlib import Path


DOCKER = """#!/bin/bash
case "$1" in
  image) [[ "$2" == inspect ]] && printf 'linux/arm64\\n' ;;
  create|run)
    if [[ "${!#}" == mcp ]]; then
      IFS= read -r request
      exit 0
    fi
    printf '%s\\n' "$@" > "$SMOKE_FIXTURE_DIR/docker-args"
    if [[ "$1" == run ]]; then
      cat "$SMOKE_FIXTURE_DIR/diagnostic" >&2
    fi
    ;;
  inspect)
    if [[ -z "${SMOKE_FIXTURE_FORCE_HEALTHCHECK:-}" ]] &&
       grep -Fxq -- --no-healthcheck "$SMOKE_FIXTURE_DIR/docker-args"; then
      printf '["NONE"]\\n'
    else
      printf '["CMD","/app/axiom-server","healthcheck"]\\n'
    fi
    ;;
  start)
    touch "$SMOKE_FIXTURE_DIR/started"
    cat "$SMOKE_FIXTURE_DIR/diagnostic" >&2
    ;;
  rm) ;;
  *) exit 2 ;;
esac
"""


class SmokeVerifierTests(unittest.TestCase):
    def setUp(self) -> None:
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.scratch = Path(directory.name)
        docker = self.scratch / "docker"
        docker.write_text(DOCKER)
        docker.chmod(0o700)
        self.root = Path(__file__).resolve().parents[1]
        version_prefix = "ARG BAML_LIB_VERSION="
        (version,) = [line.removeprefix(version_prefix)
                      for line in (self.root / "Dockerfile").read_text().splitlines()
                      if line.startswith(version_prefix)]
        self.record = json.dumps(dict(status="ok", reason="verified",
                                      platform="linux/arm64", baml_version=version)) + "\n"
        self.diagnostic = self.scratch / "diagnostic"
        self.diagnostic.write_text(self.record)

    def run_verifier(self, function: str, **settings: str) -> subprocess.CompletedProcess[str]:
        command = ["bash", "-c", "source scripts/container-smoke.sh\n"
                   "smoke_dir=$SMOKE_FIXTURE_DIR\n" + function]
        env = dict(os.environ, PATH=f"{self.scratch}:{os.environ['PATH']}",
                   PYTHONOPTIMIZE="1", SMOKE_FIXTURE_DIR=str(self.scratch))
        env.update(settings)
        with subprocess.Popen(
            command,
            cwd=self.root,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
        ) as process:
            try:
                stdout, stderr = process.communicate(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.communicate()
                self.fail(f"{function} did not exit after the Docker fixture completed")
        return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)

    def test_diagnostic_disables_healthcheck(self) -> None:
        result = self.run_verifier("check_diagnostic 0 ok verified")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--no-healthcheck", (self.scratch / "docker-args").read_text().splitlines())
        self.assertTrue((self.scratch / "started").exists())

    def test_diagnostic_refuses_to_start_with_effective_healthcheck(self) -> None:
        result = self.run_verifier("check_diagnostic 0 ok verified", SMOKE_FIXTURE_FORCE_HEALTHCHECK="1")
        self.assertNotEqual(result.returncode, 0, result.stderr)
        self.assertIn("diagnostic healthcheck is enabled", result.stderr)
        self.assertFalse((self.scratch / "started").exists())

    def test_diagnostic_rejects_invalid_stderr_with_optimization(self) -> None:
        for record in ("plain text\n", self.record.rstrip("\n"), self.record + "\n"):
            with self.subTest(record=record):
                self.diagnostic.write_text(record)
                result = self.run_verifier("check_diagnostic 0 ok verified")
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn("Invalid diagnostic", result.stderr)

    def test_mcp_rejects_early_eof_with_optimization(self) -> None:
        result = self.run_verifier("check_stdio")
        self.assertNotEqual(result.returncode, 0, result.stderr)
        self.assertIn("MCP stdout closed before the response", result.stderr)


if __name__ == "__main__":
    unittest.main()
