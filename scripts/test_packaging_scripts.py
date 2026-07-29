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
        self.assertIn("$BundleGoogleKernel", installer)
        self.assertIn("$GoogleKernelCleanupLine", installer)
        self.assertIn("Preserve any existing optional Google 148 kernel", installer)
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

    def test_installer_preserves_mutable_client_data(self):
        installer = self.read("scripts/build_installer.ps1")
        self.assertNotIn('Copy-Item -LiteralPath $ConfigSrc -Destination "$Stage\\config.yaml"', installer)
        self.assertNotIn('New-Item -ItemType Directory -Force -Path "$Stage\\data"', installer)
        self.assertIn('IfFileExists "`$INSTDIR\\config.yaml" config_present', installer)
        uninstall_section = installer.split('Section "Uninstall"', 1)[1].split("SectionEnd", 1)[0]
        self.assertNotIn('RMDir /r "`$INSTDIR"', uninstall_section)
        for protected in ["config.yaml", "data", "extensions", "chrome"]:
            self.assertNotRegex(uninstall_section, rf'(Delete|RMDir /r)[^\n]*{re.escape(protected)}')

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

    def test_release_go_tests_are_deterministic_and_logged(self):
        wrapper = self.read("scripts/build_windows_selfuse.ps1")
        speedtest = self.read("backend/test/proxy/speedtest_debug_test.go")
        self.assertIn("go test -count=1 ./...", wrapper)
        self.assertIn("go-test-windows.log", wrapper)
        self.assertTrue(
            speedtest.startswith("//go:build integration\n"),
            "external-network proxy debug tests must not run in release builds",
        )

    def test_publish_script_hydrates_shallow_tag_history(self):
        publisher = self.read("scripts/publish_windows_github_release.ps1")
        self.assertIn("git rev-parse --is-shallow-repository", publisher)
        self.assertIn("git fetch --unshallow origin --tags --force", publisher)
        self.assertIn('"$Tag^{}^"', publisher)

    def test_sync_window_collection_is_action_driven(self):
        page = self.read("frontend/src/modules/browser/pages/WindowSyncPage.tsx")
        backend = self.read("backend/app_sync_api.go")
        app_backend = self.read("backend/app.go")
        runtime_snapshot = self.read("backend/browser_runtime_snapshot_windows.go")
        main_helpers = self.read("main_runtime_helpers.go")
        self.assertNotIn("setInterval(() =>", page)
        self.assertNotIn("browser:instance:started", page)
        self.assertNotIn("browser:instance:stopped", page)
        self.assertNotIn("visibilitychange", page)
        self.assertIn("const releaseCollectedSyncData", page)
        self.assertIn("const snapshot = await getSyncSnapshot()", page)
        self.assertIn("const seq = ++loadProfilesSeq.current", page)
        self.assertNotIn("loadProfilesPromiseRef", page)
        self.assertNotIn("const freshProfiles = await loadProfiles()", page)
        self.assertIn("func (a *App) GetSyncSnapshot() SyncSnapshot", backend)
        self.assertNotIn("a.reconcileBrowserRuntimeStateOnce()", backend)
        self.assertIn("a.readBrowserRuntimeSnapshot()", backend)
        self.assertIn("func (a *App) PrepareWindowSyncRuntimeSnapshot()", runtime_snapshot)
        self.assertIn("a.App.PrepareWindowSyncRuntimeSnapshot()", main_helpers)
        self.assertIn("if !a.panelMode {", app_backend)
        self.assertIn("a.reconcileBrowserRuntimeStateOnce()", app_backend)
        self.assertNotIn("reconcileSyncRuntimeStateAsync", backend)

    def test_startup_window_template_does_not_propagate_to_extension_popups(self):
        launch = self.read("backend/app_instance.go")
        startup_bounds = self.read("backend/extension_popup_sizer_windows.go")
        popup_bounds = self.read("backend/sync_popup_confinement_windows.go")
        startup_tabs = self.read("backend/extension_startup_cleanup.go")
        launch_args = self.read("backend/browser_launch_args.go")

        self.assertNotIn('args = append(args, "--window-size=1400,600")', launch)
        self.assertNotIn('args = append(args, "about:blank")', launch)
        self.assertNotIn('return append(out, "about:blank")', launch_args)
        self.assertIn("preparePrimaryEnvironmentLaunchArgs(args)", launch)
        self.assertIn("enforceBrowserWindowBounds(profile.Pid, 1400, 600)", launch)
        self.assertIn("startup-only", startup_bounds)
        self.assertNotIn("startupWindowBoundsSession", startup_bounds)
        self.assertNotIn("time.Sleep", startup_bounds)
        self.assertNotIn("extensionPopupTargetWidth", popup_bounds)
        self.assertNotIn("extensionPopupTargetHeight", popup_bounds)
        self.assertIn("syncPopupConfinementEnabled(s.IsActive(), s.IsPaused()", popup_bounds)
        self.assertIn("search.applyPlacements()", popup_bounds)
        self.assertIn("HWND_NOTOPMOST", popup_bounds)
        self.assertIn("expectedAbove := boundary", popup_bounds)
        self.assertNotIn("if !ok && isCompactExtensionPopupTitle", popup_bounds)
        self.assertIn("WM_MOUSEWHEEL", self.read("backend/app_input_syncer.go"))
        self.assertIn("WM_MOUSEHWHEEL", self.read("backend/app_input_syncer.go"))
        self.assertIn("closeUnwantedStartupPagesOnce", startup_tabs)
        self.assertIn("planStartupPageCleanup", startup_tabs)
        self.assertNotIn("extensionStartupSettleDelay", startup_tabs)
        self.assertIn("startupPageCloseExtraBlank", startup_tabs)
        self.assertNotIn("settleBrowserStartupTabs", startup_tabs)
        self.assertIn("shouldCloseAutomaticExtensionStartupTarget", startup_tabs)
        self.assertNotIn("startupTargetMatchesProtectedURL", startup_tabs)
        self.assertIn("No DOM, form or page content is", startup_tabs)
        self.assertIn("read, and no cleanup owner survives this function", startup_tabs)
        self.assertNotIn("extensionStartupCleanupWindow", startup_tabs)
        self.assertNotIn("extensionStartupProbeDelay", startup_tabs)
        self.assertNotIn("for {", startup_tabs)
        self.assertNotIn("Target.setDiscoverTargets", startup_tabs)
        self.assertNotIn("automaticExtensionStartupGuardDuration", startup_tabs)
        self.assertNotIn("automaticExtensionStartupGuards", startup_tabs)
        self.assertNotIn("automaticExtensionStartupCleanupDelays", startup_tabs)
        self.assertNotIn("time.AfterFunc", startup_tabs)
        self.assertNotIn("go func", startup_tabs)
        self.assertEqual(launch.count("finalizeBrowserStartupTabs(stableDebugPort, profileId)"), 1)
        self.assertLess(
            launch.index("finalizeBrowserStartupTabs(stableDebugPort, profileId)"),
            launch.index("navigateToTargetURLs(stableDebugPort"),
        )
        self.assertNotIn(
            "finalizeBrowserStartupTabs",
            self.read("backend/browser_runtime_state.go"),
        )

    def test_extension_distribution_is_explicit_and_never_profile_or_startup_driven(self):
        app = self.read("backend/app.go")
        launch = self.read("backend/app_instance.go")
        extension_backend = self.read("backend/app_extension_import.go")
        page = self.read("frontend/src/modules/browser/pages/ExtensionManagementPage.tsx")

        self.assertNotIn("appendGlobalExtensionArgsForNewProfile", app)
        self.assertNotIn("appendGlobalExtensionArgsForNewProfile", extension_backend)
        self.assertNotIn("loadGlobalExtensionRegistry", launch)
        self.assertNotIn("BrowserGlobalExtensionImport", launch)

        submit_body = page.split("const submitExtension = async () => {", 1)[1].split(
            "const distributeExtension = async", 1
        )[0]
        distribute_body = page.split("const distributeExtension = async", 1)[1].split(
            "const removeExtension = async", 1
        )[0]
        self.assertNotIn("importGlobalExtension(", submit_body)
        self.assertIn("importGlobalExtension(item.downloadAddress)", distribute_body)
        self.assertIn("只有点击“分配”才会检测并安装", submit_body)

    def test_profile_delete_removes_owned_data_and_sync_accepts_arranged_windows(self):
        manager = self.read("backend/internal/browser/profile.go")
        page = self.read("frontend/src/modules/browser/pages/BrowserListPage.tsx")
        sync_page = self.read("frontend/src/modules/browser/pages/WindowSyncPage.tsx")
        sync_api = self.read("backend/app_sync_api.go")
        win32 = self.read("backend/win32_helpers.go")

        self.assertIn("return m.DeleteWithCache(profileId, true)", manager)
        self.assertIn("func (m *Manager) DeleteWithCache(profileId string, _ bool)", manager)
        archive = self.read("backend/internal/browser/profile_archive.go")
        self.assertIn("stageProfileDataArchiveLocked", manager)
        self.assertIn("os.Rename(originalDir, archiveDir)", archive)
        self.assertIn("os.Rename(move.archiveDir, move.originalDir)", archive)
        self.assertNotIn("os.RemoveAll(userDataDir)", manager)
        self.assertNotIn("deleteCache", page)
        self.assertIn("本机恢复归档", page)

        self.assertIn("cachedWindows := make(map[string]windows.HWND", sync_api)
        self.assertIn("cachedWindows[profile.ProfileId] == 0", sync_api)
        self.assertIn("browserTopLevelClientSizeAllowed", win32)
        self.assertNotIn("clientW < 320 || clientH < 240", win32)
        self.assertNotIn("loadProfilesPromiseRef", sync_page)

    def test_environment_close_has_one_graceful_data_flush_owner(self):
        instance = self.read("backend/app_instance.go")
        shutdown = self.read("backend/app_shutdown.go")
        sync_api = self.read("backend/app_sync_api.go")
        pointer = self.read("backend/internal/browser/profile_data_pointer.go")
        process_windows = self.read("backend/process_liveness_windows.go")

        stop_body = instance.split("func (a *App) BrowserInstanceStop", 1)[1].split(
            "func (a *App) BrowserInstanceRestart", 1
        )[0]
        self.assertIn('WriteProfileDataPointer(profileSnapshot, "closing"', stop_body)
        self.assertIn("tryCloseBrowserViaCDP", stop_body)
        self.assertIn("waitEnvironmentDataFlush", stop_body)
        self.assertIn('WriteProfileDataPointer(profileSnapshot, "closed"', stop_body)
        self.assertNotIn("Process.Kill", stop_body)
        self.assertNotIn('"/F"', stop_body)
        self.assertIn("BrowserInstanceStop(profileID)", shutdown)
        self.assertIn("BrowserInstanceStop(profileID)", sync_api)
        self.assertNotIn("Process.Kill", shutdown)
        self.assertNotIn('exec.Command("taskkill"', process_windows)
        self.assertIn("WM_CLOSE", process_windows)
        self.assertIn('WriteProfileDataPointer(closeSnapshot, "closed"', instance)
        self.assertIn("ProfileDataPointerFileName", pointer)
        self.assertIn("fsutil.WriteFileAtomic", pointer)
        self.assertNotIn("Cookies", pointer.split("type ProfileDataPointer struct", 1)[1])

    def test_go_mod_does_not_replace_modules_with_missing_local_third_party_dirs(self):
        text = self.read("go.mod")
        self.assertNotRegex(text, r"replace\s+github\.com/energye/systray\s+=>\s+\./third_party/systray")
        self.assertNotIn("./third_party/systray", text)

    def test_backend_cloak_helpers_restore_windows_build_symbols(self):
        text = self.read("backend/cloak_integration_helpers.go")
        for symbol in [
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
        self.assertNotIn("func resolveCloakGeoArgs", text)

    def test_environment_startup_has_no_obsolete_background_tab_collector(self):
        runtime_state = self.read("backend/browser_runtime_state.go")
        instance = self.read("backend/app_instance.go")
        lock_guard = self.read("backend/browser_profile_lock_guard_windows.go")

        self.assertNotIn("startLastTabsTracker", runtime_state)
        self.assertNotIn("stopLastTabsTracker", instance)
        self.assertNotIn("captureRestorableTabsViaCDP", instance)
        self.assertNotIn("stopWindowBoundsTrackerAndFinalize", instance)
        self.assertIn("browserSingletonArtifactsPresent(userDataDir)", lock_guard)
        self.assertLess(
            lock_guard.index("browserSingletonArtifactsPresent(userDataDir)"),
            lock_guard.index("failIfUserDataDirOwnedByLiveBrowser(userDataDir)"),
        )

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
        self.assertIn("Require-SupportedNodeVersion", text)
        self.assertIn("Node.js 22 LTS is required", text)
        self.assertIn("UV_HANDLE_CLOSING", text)

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
            "OpenJS.NodeJS.22",
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
        self.assertIn("[int]$nodeMatch.Groups[1].Value -ne 22", text)

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
        self.assertIn("(-not $ManagerOnly)", installer)
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
        self.assertIn("'docs/DELETION_LEDGER.md'", text)
        self.assertIn("function Invoke-GhProbe", text)
        self.assertIn("$existingProbe.ExitCode", text)
        self.assertIn("scripts\\check_code_health.ps1", text)
        self.assertIn("git describe --tags --abbrev=0 --match 'v[0-9]*' \"$Tag^{}^\"", text)
        self.assertIn("[string]$ApprovedGrowthReason", text)
        self.assertIn("'-ApprovedGrowthReason', $ApprovedGrowthReason", text)
        self.assertIn("'-BaseRef',", text)
        self.assertIn("$PreviousTag", text)
        self.assertNotIn("$existingText = & gh release view", text)
        self.assertIn("activation-check.exe", text)
        self.assertIn("BrowserStudio-Private-Setup-v$Version.exe", text)
        assets_block = text.split("$assets = @(", 1)[1].split(")", 1)[0]
        self.assertNotIn("activation-check.exe", assets_block)
        self.assertNotIn("BrowserStudio-Private-Setup", assets_block)

    def test_code_health_guard_requires_a_reversible_deletion_ledger(self):
        raw = (ROOT / "scripts/check_code_health.ps1").read_bytes()
        self.assertTrue(all(byte < 128 for byte in raw), "code health guard must remain ASCII-only")
        text = raw.decode("ascii")
        self.assertIn("docs/DELETION_LEDGER.md", text)
        self.assertIn("LedgerDeletionThreshold", text)
        self.assertIn("LedgerDeletionThreshold = 20", text)
        self.assertIn("--diff-filter=D", text)
        self.assertIn("MaxNetGrowth", text)
        self.assertIn("Retired window-watcher code was reintroduced", text)

        policy = self.read("docs/CHANGE_POLICY.md")
        agents = self.read("AGENTS.md")
        self.assertIn("Replace, then remove", policy)
        self.assertIn("Recovery after an incorrect deletion", policy)
        self.assertIn("Do not stack a new workaround", agents)

        ledger = self.read("docs/DELETION_LEDGER.md")
        for field in ["Last known revision", "Reason", "Replacement", "Verification", "Precise recovery"]:
            self.assertIn(field, ledger)

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
