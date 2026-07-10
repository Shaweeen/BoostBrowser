import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class PackagingScriptsTest(unittest.TestCase):
    def read(self, rel: str) -> str:
        return (ROOT / rel).read_text(encoding="utf-8")

    def test_build_scripts_do_not_depend_on_old_z_drive_deployments(self):
        for rel in ["scripts/build_release.ps1", "scripts/build_installer.ps1", "scripts/stage_assets.ps1"]:
            text = self.read(rel)
            self.assertNotIn("Z:\\", text, f"{rel} still depends on an old machine-specific Z: path")
            self.assertNotIn("BoostBrowser_v110_test", text, f"{rel} still names the old staging deployment")

    def test_cloakbrowser_download_script_documents_official_free_release(self):
        text = self.read("scripts/install_cloakbrowser_kernel.ps1")
        self.assertIn("CloakHQ/CloakBrowser", text)
        self.assertIn("chromium-v146.0.7680.177.5", text)
        self.assertIn("cloakbrowser-windows-x64.zip", text)
        self.assertIn("SHA256SUMS", text)
        self.assertRegex(text, r"chrome\\cloak-146\.0\.7680\.177")

    def test_installer_uses_repo_local_standard_asset_layout(self):
        text = self.read("scripts/build_installer.ps1")
        self.assertRegex(text, r"\$AssetRoot\s*=\s*if \(\$env:BOOST_KERNEL_SRC\)")
        self.assertRegex(text, r"chrome\\cloak-146\.0\.7680\.177")
        self.assertRegex(text, r"chrome\\google-148\.0\.7778\.167")
        self.assertNotIn("CloakKernelSrc  = 'Z:", text)
        self.assertNotIn("GoogleKernelSrc = 'Z:", text)

    def test_stage_assets_allows_optional_google_and_extensions(self):
        text = self.read("scripts/stage_assets.ps1")
        self.assertIn("required", text.lower())
        self.assertIn("optional", text.lower())
        self.assertIn("backend\\embedded_extensions\\chromium-web-store", text)
        self.assertIn("if (-not (Test-Path $src))", text)
    def test_no_active_script_keeps_old_machine_specific_paths(self):
        for path in (ROOT / "scripts").rglob("*"):
            if not path.is_file() or path.name == "test_packaging_scripts.py":
                continue
            text = path.read_text(encoding="utf-8", errors="ignore")
            self.assertNotIn("Z:\\", text, f"{path.relative_to(ROOT)} still has old machine-specific Z: paths")
            self.assertNotIn("Ant-Browser", text, f"{path.relative_to(ROOT)} still references old Ant-Browser staging names")
            self.assertNotIn("BoostBrowser_v110_test", text, f"{path.relative_to(ROOT)} still names old staging deployment")


if __name__ == "__main__":
    unittest.main()
