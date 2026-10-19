import importlib.util
import sys
import unittest
from pathlib import Path
from unittest.mock import patch


class E2EImportTest(unittest.TestCase):
    def test_import_does_not_parse_arguments_or_contact_services(self):
        path = Path(__file__).with_name("e2e.py")
        spec = importlib.util.spec_from_file_location("e2e_import_test", path)
        module = importlib.util.module_from_spec(spec)
        with (
            patch.dict(sys.modules, {spec.name: module}),
            patch.object(sys, "argv", [str(path)]),
            patch(
                "argparse.ArgumentParser.parse_args",
                side_effect=AssertionError("parsed CLI on import"),
            ),
            patch(
                "subprocess.run", side_effect=AssertionError("ran command on import")
            ),
            patch(
                "urllib.request.urlopen",
                side_effect=AssertionError("HTTP request on import"),
            ),
        ):
            spec.loader.exec_module(module)


if __name__ == "__main__":
    unittest.main()
