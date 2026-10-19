import contextlib
import importlib.util
import io
import runpy
import sys
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT_PATH = Path(__file__).with_name("2pc-recovery.py")


class RecoveryCommandLineTest(unittest.TestCase):
    def test_import_does_not_parse_arguments_or_contact_services(self):
        spec = importlib.util.spec_from_file_location(
            "recovery_import_test", SCRIPT_PATH
        )
        module = importlib.util.module_from_spec(spec)
        with (
            patch.dict(sys.modules, {spec.name: module}),
            patch.object(sys, "argv", [str(SCRIPT_PATH)]),
            patch(
                "argparse.ArgumentParser.parse_args",
                side_effect=AssertionError("parsed CLI on import"),
            ),
            patch(
                "subprocess.run", side_effect=AssertionError("ran command on import")
            ),
            patch(
                "subprocess.Popen",
                side_effect=AssertionError("spawned process on import"),
            ),
            patch(
                "urllib.request.urlopen",
                side_effect=AssertionError("HTTP request on import"),
            ),
        ):
            spec.loader.exec_module(module)

    def test_wrong_project_is_rejected_before_external_actions(self):
        stderr = io.StringIO()
        with (
            patch.object(
                sys, "argv", [str(SCRIPT_PATH), "--project", "marketplace-production"]
            ),
            patch("subprocess.run", side_effect=AssertionError("ran command")),
            patch("subprocess.Popen", side_effect=AssertionError("spawned process")),
            patch("urllib.request.urlopen", side_effect=AssertionError("HTTP request")),
            contextlib.redirect_stderr(stderr),
            self.assertRaises(SystemExit) as exit_status,
        ):
            runpy.run_path(str(SCRIPT_PATH), run_name="__main__")

        self.assertEqual(exit_status.exception.code, 2)
        self.assertIn(
            "only the dedicated marketplace-2pc-check project is allowed",
            stderr.getvalue(),
        )


if __name__ == "__main__":
    unittest.main()
