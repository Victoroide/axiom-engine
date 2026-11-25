import json
import os
import signal
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path


DOCKER = """#!/bin/bash
case "$1" in
  image)
    if [[ "$5" == '{{json .Config}}' ]]; then
      printf '{"Entrypoint":["/app/axiom-server"],"WorkingDir":"/app","Healthcheck":{"Test":["CMD","/app/axiom-server","healthcheck"]}}\\n'
    else
      [[ "$2" == inspect ]] && printf 'linux/arm64\\n'
    fi
    ;;
  top) printf 'PID COMMAND\\n42 axiom-server\\n' ;;
  cp) cat "$SMOKE_FIXTURE_DIR/app.tar" ;;
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
    if [[ "$3" == '{{.State.Pid}} {{.Path}} {{len .Args}}' ]]; then
      printf '42 /app/axiom-server 0\\n'
    elif [[ -z "${SMOKE_FIXTURE_FORCE_HEALTHCHECK:-}" ]] &&
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

    def check_image_contract(self, inventory: bool, diagnostic: bool,
                             *extra_files: tuple[str, bytes, int]) -> subprocess.CompletedProcess[str]:
        with tarfile.open(self.scratch / "app.tar", "w") as archive:
            for name, kind, mode in (
                (".", tarfile.DIRTYPE, 0o755),
                ("axiom-server", tarfile.REGTYPE, 0o755),
                ("libbaml_cffi.so", tarfile.REGTYPE, 0o644),
                *extra_files,
            ):
                entry = tarfile.TarInfo(name)
                entry.type = kind
                entry.mode = mode
                archive.addfile(entry)
        return self.run_verifier(
            f"name=fixture\ninclude_diagnostic={int(diagnostic)}\ncheck_image_contract",
            RELEASE_NATIVE_INVENTORY=str(inventory).lower(),
        )

    def test_inventory_image_requires_exact_regular_inventory(self) -> None:
        filename = "baml-native.cdx.json"
        result = self.check_image_contract(True, False, (filename, tarfile.REGTYPE, 0o644))
        self.assertEqual(result.returncode, 0, result.stderr)
        for files in ((), ((filename, tarfile.REGTYPE, 0o755),), ((filename, tarfile.SYMTYPE, 0o644),),
                      ((filename, tarfile.REGTYPE, 0o644), ("extra", tarfile.REGTYPE, 0o644))):
            with self.subTest(files=files):
                result = self.check_image_contract(True, False, *files)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn("AssertionError", result.stderr)

    def test_default_and_diagnostic_images_reject_inventory(self) -> None:
        inventory_file = ("baml-native.cdx.json", tarfile.REGTYPE, 0o644)
        for diagnostic, files in ((False, ()), (True, (("axiom-baml-check", tarfile.REGTYPE, 0o755),))):
            with self.subTest(diagnostic=diagnostic):
                result = self.check_image_contract(False, diagnostic, *files)
                self.assertEqual(result.returncode, 0, result.stderr)
                result = self.check_image_contract(False, diagnostic, *files, inventory_file)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn("AssertionError", result.stderr)


if __name__ == "__main__":
    unittest.main()
