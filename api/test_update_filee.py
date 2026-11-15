"""Release checks use synthetic public metadata only; never build or deploy."""
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

spec = importlib.util.spec_from_file_location("update_filee", Path(__file__).with_name("update-filee.py"))
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


class ReleaseDownloadTests(unittest.TestCase):
    def test_gateway_timeout_retries_then_returns_the_original_response(self):
        error = urllib.error.HTTPError("https://fixture.invalid", 504, "Gateway Timeout", {}, None)
        with patch.object(updater.urllib.request, "urlopen", side_effect=[error, io.BytesIO(b"verified metadata")]) as opening, patch.object(updater.time, "sleep") as sleep:
            self.assertEqual(updater.get("https://fixture.invalid"), b"verified metadata")
            self.assertEqual(opening.call_count, 2)
            sleep.assert_called_once_with(1)

    def test_transient_network_failures_have_a_finite_retry_budget(self):
        with patch.object(updater.urllib.request, "urlopen", side_effect=urllib.error.URLError("unavailable")) as opening, patch.object(updater.time, "sleep") as sleep:
            with self.assertRaises(urllib.error.URLError):
                updater.get("https://fixture.invalid")
            self.assertEqual(opening.call_count, 3)
            self.assertEqual([call.args[0] for call in sleep.call_args_list], [1, 2])

    def test_missing_release_is_not_retried(self):
        error = urllib.error.HTTPError("https://fixture.invalid", 404, "Not Found", {}, None)
        with patch.object(updater.urllib.request, "urlopen", side_effect=error) as opening, patch.object(updater.time, "sleep") as sleep:
            with self.assertRaises(urllib.error.HTTPError):
                updater.get("https://fixture.invalid")
            self.assertEqual(opening.call_count, 1)
            sleep.assert_not_called()

    def test_oversized_metadata_is_rejected_without_retry(self):
        with patch.object(updater.urllib.request, "urlopen", return_value=io.BytesIO(b"x" * (2 * 1024 * 1024 + 1))) as opening, patch.object(updater.time, "sleep") as sleep:
            with self.assertRaisesRegex(ValueError, "exceeds"):
                updater.get("https://fixture.invalid")
            self.assertEqual(opening.call_count, 1)
            sleep.assert_not_called()

    def test_a_later_download_failure_preserves_the_entire_dockerfile(self):
        release = json.dumps(dict(tag_name="v2.4.1", assets=[dict(name="Filee-2.4.1-linux-x64-SHA256SUMS.txt", browser_download_url="https://fixture.invalid/checksums")])).encode()
        checksum = ("a" * 64 + "  Filee-2.4.1-linux-x64.tar.gz\n").encode()
        with tempfile.TemporaryDirectory() as folder:
            script = Path(folder) / "update-filee.py"
            dockerfile = script.with_name("Dockerfile")
            original = b"ARG FILEE_VERSION=2.3.0\nARG FILEE_SHA256=old\n"
            dockerfile.write_bytes(original)
            with patch.object(updater, "__file__", str(script)), patch.object(updater, "get", side_effect=[release, checksum, urllib.error.URLError("unavailable")]):
                with self.assertRaises(urllib.error.URLError):
                    updater.main()
            self.assertEqual(dockerfile.read_bytes(), original)


if __name__ == "__main__":
    unittest.main()
