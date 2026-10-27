"""Real pinned clients against invented local protocol fixtures; explicit opt-in."""

import os
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]


@unittest.skipUnless(os.environ.get("RUN_SYNTHETIC_PROTOCOL_CONTRACT_TESTS") == "1",
                     "local synthetic protocol execution is opt-in")
class ProtocolContracts(unittest.TestCase):
    def test_real_clients_tls_and_rejected_destinations(self):
        # The fixed source program starts only ephemeral loopback listeners and
        # implements no external forwarding. It never reads a production file.
        result = subprocess.run(
            ["go", "run", "../rehearsal/application/mock_services/main.go", "self-test"],
            cwd=ROOT / "backend", capture_output=True, text=True, timeout=600,
            check=False,
        )
        self.assertEqual(result.returncode, 0,
                         "synthetic protocol contracts failed: " + result.stderr[-3000:])
        self.assertEqual(result.stdout.strip(), "synthetic protocol contracts verified")


if __name__ == "__main__":
    unittest.main()
