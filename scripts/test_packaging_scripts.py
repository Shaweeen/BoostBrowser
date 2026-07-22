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
        raw = (ROOT / "scripts/build_installer.ps1").read_bytes()
        # Windows PowerShell 5.1 can misread UTF-8 without BOM. Keep this
        # script ASCII-only so localized text cannot corrupt quotes/braces.
        self.assertTrue(all(byte < 128 for byte in raw), "build_installer.ps1 must remain ASCII-only")
        text = raw.decode("ascii")
        self.assertRegex(text, r"\$AssetRoot\s*=\s*if \(\$env:BOOST_KERNEL_SRC\)")
        self.assertRegex(text, r"chrome\\cloak-146\.0\.7680\.177")
        self.assertRegex(text, r"chrome\\google-148\.0\.7778\.167")
        self.assertNotIn("CloakKernelSrc  = 'Z:", text)
        self.assertNotIn("GoogleKernelSrc = 'Z:", text)

    def test_google_148_fallback_uses_extension_compatible_chrome_for_testing(self):
        installer = self.read("scripts/build_installer.ps1")
        wrapper = self.read("scripts/build_windows_selfuse.ps1")
        downloader = self.read("scripts/install_chrome_for_testing_kernel.ps1")
        stage_assets = self.read("scripts/stage_assets.ps1")

        self.assertIn("chrome-for-testing.marker", installer)
        self.assertIn('RMDir /r "`$INSTDIR\\chrome\\google-148.0.7778.167"', installer)
        self.assertIn("chrome-for-testing.marker", wrapper)
        self.assertIn("install_chrome_for_testing_kernel.ps1", wrapper)
        self.assertNotIn("C:\\Program Files\\Google\\Chrome\\Application", wrapper)
        self.assertIn("chrome-for-testing-public/$Version/win64/chrome-win64.zip", downloader)
        self.assertIn('Version = "148.0.7778.167"', downloader)
        self.assertIn("chrome-for-testing.marker", stage_assets)

    def test_installer_bundles_and_conditionally_installs_windows_runtimes(self):
        installer = self.read("scripts/build_installer.ps1")
        self.assertIn("Get-MicrosoftPrerequisite", installer)
        self.assertIn("Get-AuthenticodeSignature", installer)
        self.assertIn("Microsoft Corporation", installer)
        self.assertIn("MicrosoftEdgeWebview2Setup.exe", installer)
        self.assertIn("VC_redist.x64.exe", installer)
        self.assertIn("Function EnsureWebView2Runtime", installer)
        self.assertIn("Function EnsureVCRuntime", installer)
        self.assertIn("F3017226-FE2A-4295-8BDF-00C3A9A7E4C5", installer)
        self.assertIn("VC\\Runtimes\\x64", installer)
        self.assertIn("Call EnsureVCRuntime", installer)
        self.assertIn("Call EnsureWebView2Runtime", installer)

    def test_installer_only_stops_browserstudio_owned_processes(self):
        installer = self.read("scripts/build_installer.ps1")
        cleanup = self.read("scripts/close_browserstudio_processes.ps1")
        self.assertIn("close-browserstudio-processes.ps1", installer)
        self.assertIn("Function un.onInit", installer)
        self.assertIn("Function un.CloseBoostProcesses", installer)
        self.assertIn("Call un.CloseBoostProcesses", installer)
        uninstall_section = installer.split('Section "Uninstall"', 1)[1].split("SectionEnd", 1)[0]
        self.assertNotIn("Call CloseBoostProcesses", uninstall_section)
        self.assertNotIn('/IM chrome.exe', installer)
        self.assertNotIn('/IM xray.exe', installer)
        self.assertNotIn('/IM sing-box.exe', installer)
        self.assertIn("ExecutablePath", cleanup)
        self.assertIn("$rootPrefix + 'chrome\\'", cleanup)
        self.assertIn("$rootPrefix + 'bin\\'", cleanup)

    def test_release_versions_are_consistent(self):
        import json

        wails = json.loads(self.read("wails.json"))
        package = json.loads(self.read("frontend/package.json"))
        lock = json.loads(self.read("frontend/package-lock.json"))
        version = wails["info"]["productVersion"]
        self.assertEqual(package["version"], version)
        self.assertEqual(lock["version"], version)
        self.assertEqual(lock["packages"][""]["version"], version)

    def test_go_mod_does_not_replace_modules_with_missing_local_third_party_dirs(self):
        text = self.read("go.mod")
        self.assertNotRegex(text, r"replace\s+github\.com/energye/systray\s+=>\s+\./third_party/systray")
        self.assertNotIn("./third_party/systray", text)

    def test_backend_cloak_helpers_restore_windows_build_symbols(self):
        text = self.read("backend/cloak_integration_helpers.go")
        for symbol in [
            "func resolveCloakGeoArgs",
            "func isCloakCore",
            "func buildEffectiveFingerprintArgs",
            "func seedDefaultSearchEngine",
            "func launchArgKey",
            "func seedDefaultSearchEngineViaCDPWithRetry",
            "func injectStealthToAllPagesWithUA",
            "func (a *App) StartInstance",
            "func (a *App) StartInstanceWithParams",
        ]:
            self.assertIn(symbol, text)

    def test_main_runtime_helpers_restore_clean_checkout_build_symbols(self):
        text = self.read("main_runtime_helpers.go")
        for symbol in [
            "var syncPanelMode",
            "func hasCLIArg",
            "func takeoverExistingMainInstanceForPostUpdate",
            "func restoreNativeMainWindowBounds",
            "func (a *App) IsWindowSyncPanelMode",
            "func (a *App) SaveNativeMainWindowBounds",
            "func (a *App) OpenWindowSyncPanel",
        ]:
            self.assertIn(symbol, text)

    def test_stage_assets_keeps_helper_extension_out_of_clean_package(self):
        installer = self.read("scripts/build_installer.ps1")
        self.assertIn("Helper extension is intentionally not bundled", installer)
        self.assertNotIn("embedded_extensions\\chromium-web-store", installer)

    def test_one_click_windows_build_orchestrates_standard_flow(self):
        text = self.read("scripts/build_windows_selfuse.ps1")
        self.assertIn("install_cloakbrowser_kernel.ps1", text)
        self.assertIn("npm ci", text)
        self.assertIn("npm run build", text)
        self.assertIn("go mod download", text)
        self.assertIn("go test -c", text)
        self.assertIn("build_release.ps1", text)
        self.assertIn("build_installer.ps1", text)
        self.assertIn("BOOST_KERNEL_SRC", text)
        self.assertIn("google-148.0.7778.167", text)
        self.assertIn("[switch]$NoInstall", text)
        self.assertIn('Run-Step "Starting Setup installer"', text)
        self.assertIn("Start-Process -FilePath $setupPath -Wait -PassThru", text)
        self.assertIn('Require-MinimumVersion "Go"', text)
        self.assertIn("1.25.0", text)

    def test_release_build_emits_hashes_and_manifest(self):
        text = self.read("scripts/build_release.ps1")
        self.assertIn("@('boost-browser.exe', 'updater.exe', 'activation-check.exe')", text)
        self.assertIn('"$filePath.sha256"', text)
        self.assertIn("release-manifest.json", text)
        self.assertIn("ConvertTo-Json", text)

    def test_new_windows_private_setup_installs_and_builds_private_edition(self):
        text = self.read("scripts/setup_new_windows_private.ps1")
        for package in [
            "Git.Git",
            "GoLang.Go",
            "OpenJS.NodeJS.LTS",
            "NSIS.NSIS",
            "Microsoft.EdgeWebView2Runtime",
            "Microsoft.VCRedist.2015+.x64",
        ]:
            self.assertIn(package, text)
        self.assertIn("go.mod", text)
        self.assertIn("build_windows_selfuse.ps1", text)
        self.assertIn("BrowserStudio-Private-Setup", text)
        self.assertIn("Get-FileHash", text)
        self.assertIn("-1978335189", text)
        self.assertIn("already installed and current", text)
        self.assertIn("--source winget", text)
        self.assertIn('"-NoInstall"', text)

    def test_public_manager_build_never_bundles_third_party_runtimes(self):
        wrapper = self.read("scripts/build_windows_public.ps1")
        installer = self.read("scripts/build_installer.ps1")
        public_config = self.read("config.public.yaml")

        self.assertIn("-ManagerOnly", wrapper)
        self.assertIn("-SkipKernelInstall", wrapper)
        self.assertIn("-SkipGoogleFallback", wrapper)
        self.assertIn("-NoInstall", wrapper)
        self.assertIn("if (-not $ManagerOnly -and (Test-Path -LiteralPath $BinSrc))", installer)
        self.assertIn("if (-not $ManagerOnly) {", installer)
        self.assertIn("BrowserStudio-Manager-Setup", installer)
        self.assertRegex(public_config, r"(?m)^\s*cores:\s*\[\]\s*$")
        self.assertRegex(public_config, r"(?m)^\s*proxies:\s*\[\]\s*$")
        self.assertNotIn("cloak-146", public_config)
        self.assertNotIn("google-148", public_config)

    def test_github_release_publisher_excludes_private_assets(self):
        raw = (ROOT / "scripts/publish_windows_github_release.ps1").read_bytes()
        self.assertTrue(all(byte < 128 for byte in raw), "release publisher must remain ASCII-only for Windows PowerShell 5.1")
        text = raw.decode("ascii")
        self.assertIn("BrowserStudio-Update-v$Version-windows-x64.zip", text)
        self.assertIn("BrowserStudio-Manager-Setup-v$Version.exe", text)
        self.assertIn("--verify-tag", text)
        self.assertIn("--draft", text)
        self.assertIn("--draft=false", text)
        self.assertIn("boost-browser.exe", text)
        self.assertIn('"$MainExe.sha256"', text)
        self.assertIn("release-manifest.json", text)
        self.assertIn("BrowserStudio-Repair-Upgrade-v$Version.ps1", text)
        self.assertIn("scripts/repair_upgrade_windows.ps1", text)
        self.assertIn("packagingCommit", text)
        self.assertIn("allowedPostTagFiles", text)
        self.assertIn("function Invoke-GhProbe", text)
        self.assertIn("$existingProbe.ExitCode", text)
        self.assertNotIn("$existingText = & gh release view", text)
        self.assertIn("activation-check.exe", text)
        self.assertIn("BrowserStudio-Private-Setup-v$Version.exe", text)
        assets_block = text.split("$assets = @(", 1)[1].split(")", 1)[0]
        self.assertNotIn("activation-check.exe", assets_block)
        self.assertNotIn("BrowserStudio-Private-Setup", assets_block)

    def test_repair_upgrade_preserves_user_data_and_validates_release_files(self):
        raw = (ROOT / "scripts/repair_upgrade_windows.ps1").read_bytes()
        self.assertTrue(all(byte < 128 for byte in raw), "repair upgrader must remain ASCII-only for Windows PowerShell 5.1")
        text = raw.decode("ascii")
        import json

        version = json.loads(self.read("wails.json"))["info"]["productVersion"]
        self.assertIn(f"TargetVersion = 'v{version}'", text)
        self.assertIn("api.github.com/repos/$Owner/$Repo/releases/tags/$Tag", text)
        self.assertIn("Assert-TrustedAssetURL", text)
        self.assertIn("Get-FileHash", text)
        self.assertIn("Assert-WindowsPE", text)
        self.assertIn("data\\repair-backups", text)
        self.assertIn("Backup-CriticalState", text)
        self.assertIn("CloseMainWindow", text)
        self.assertNotIn("Stop-Process -Force", text)
        self.assertIn("Copy-Item -LiteralPath (Join-Path $tempRoot 'boost-browser.exe')", text)
        self.assertIn("Copy-Item -LiteralPath (Join-Path $tempRoot 'updater.exe')", text)
        for protected in ["config.yaml", "proxies.yaml", "chrome", "extensions"]:
            self.assertNotRegex(text, rf"Remove-Item[^\n]*{re.escape(protected)}")
        self.assertNotIn("RMDir", text)
        self.assertNotIn("Uninstall.exe", text)

    def test_no_active_script_keeps_old_machine_specific_paths(self):
        for path in (ROOT / "scripts").rglob("*"):
            if (
                not path.is_file()
                or path.name == "test_packaging_scripts.py"
                or "__pycache__" in path.parts
            ):
                continue
            text = path.read_text(encoding="utf-8", errors="ignore")
            self.assertNotIn("Z:\\", text, f"{path.relative_to(ROOT)} still has old machine-specific Z: paths")
            self.assertNotIn("Ant-Browser", text, f"{path.relative_to(ROOT)} still references old Ant-Browser staging names")
            self.assertNotIn("BoostBrowser_v110_test", text, f"{path.relative_to(ROOT)} still names old staging deployment")


if __name__ == "__main__":
    unittest.main()
