# One-shot: finish 1.7.133 start-path native extension load + version bump.
# Does not touch Cookies, Preferences, LES, wallets.
from pathlib import Path

root = Path(".")


def must_replace(path: Path, old: str, new: str):
    text = path.read_text(encoding="utf-8")
    if old not in text:
        raise SystemExit("missing block in %s" % path)
    if new.strip() in text and old not in text:
        return
    path.write_text(text.replace(old, new, 1), encoding="utf-8")


inst = root / "backend" / "app_instance.go"
text = inst.read_text(encoding="utf-8")
old_comment = (
    "\t// A legacy environment can keep the only verified copy of an assigned\n"
    "\t// extension in its saved --load-extension path. Do not strip that path: doing\n"
    "\t// so makes the extension disappear while its Chrome-owned wallet/extension\n"
    "\t// state remains in data. These arguments are used for this launch only; no\n"
    "\t// Preferences, Local State, Cookies, or extension storage is rewritten.\n"
)
new_comment = (
    "\t// A legacy environment can keep the only verified copy of an assigned\n"
    "\t// extension in its saved --load-extension path. Keep that path only\n"
    "\t// until Chrome already has a loadable Preferences entry AND durable\n"
    "\t// LES/runtime files (canSkipLoadExtensionCLI). Re-injecting after that\n"
    "\t// re-fires onInstalled and opens Rabby/MetaMask Notification homepages\n"
    "\t// on every start. Preferences / LES / Cookies / wallets are not written.\n"
)
if old_comment in text:
    text = text.replace(old_comment, new_comment, 1)

needle = "\targs, recoveredUserExtensions := appendProfileExtensionRecoveryLaunchArgs(args, userDataDir)\n\targs = normalizeLoadExtensionArgs(args)\n"
insert = needle + (
    "\t// Read-only: drop --load-extension for packages Chrome can already load\n"
    "\t// from Preferences+LES. First-adapt packages keep CLI. This is the owner\n"
    "\t// that stops Rabby/MetaMask homepage Notification windows on hot start.\n"
    "\targs, nativePresent, nativeNeedCLI := applyProfileNativeExtensionLaunchArgs(args, userDataDir)\n"
    "\tif nativePresent > 0 || nativeNeedCLI > 0 {\n"
    "\t\tlog.Info(\"\u542f\u52a8\u6269\u5c55\u52a0\u8f7d\u7b56\u7565\uff08\u53ea\u8bfb\uff0c\u4e0d\u6539\u5199\u7528\u6237\u6570\u636e\uff09\",\n"
    "\t\t\tlogger.F(\"profile_id\", profileId),\n"
    "\t\t\tlogger.F(\"native_present\", nativePresent),\n"
    "\t\t\tlogger.F(\"need_cli\", nativeNeedCLI),\n"
    "\t\t)\n"
    "\t}\n"
)
if "applyProfileNativeExtensionLaunchArgs(args, userDataDir)" not in text:
    if needle not in text:
        raise SystemExit("instance recovery block missing")
    text = text.replace(needle, insert, 1)

dismiss = (
    "\t\t\tenforceMainEnvironmentWindowOnStart(profile.Pid)\n"
    "\t\t\t// Auto-opened wallet Notification hosts (onInstalled homepage) are\n"
    "\t\t\t// not the user's connect-wallet click. Close those titled hosts\n"
    "\t\t\t// via WM_CLOSE for a short window only \u2014 no CDP, no file writes.\n"
    "\t\t\tgo dismissStartupWalletNotificationHosts(profile.Pid)\n"
)
if "dismissStartupWalletNotificationHosts(profile.Pid)" not in text:
    old = "\t\t\tenforceMainEnvironmentWindowOnStart(profile.Pid)\n"
    if old not in text:
        raise SystemExit("enforceMain call missing")
    text = text.replace(old, dismiss, 1)
inst.write_text(text, encoding="utf-8")

classify = root / "backend" / "extension_window_classify.go"
ct = classify.read_text(encoding="utf-8")
if "func isAutoOpenedWalletHomepageTitle" not in ct:
    marker = "\treturn w >= 200 && h >= 100\n}\n"
    fn = (
        "\treturn w >= 200 && h >= 100\n}\n\n"
        "// isAutoOpenedWalletHomepageTitle matches MV3 wallet *Notification* hosts that\n"
        "// Chrome opens by itself on start (onInstalled / session leftover). It does\n"
        "// not match the toolbar popup titled only \"Rabby Wallet\" / \"MetaMask\".\n"
        "func isAutoOpenedWalletHomepageTitle(title string) bool {\n"
        "\tlower := strings.ToLower(strings.TrimSpace(title))\n"
        "\tif lower == \"\" {\n"
        "\t\treturn false\n"
        "\t}\n"
        "\treturn strings.Contains(lower, \"wallet\") && strings.Contains(lower, \"notification\")\n"
        "}\n"
    )
    if marker not in ct:
        raise SystemExit("classify marker missing")
    classify.write_text(ct.replace(marker, fn, 1), encoding="utf-8")

testp = root / "backend" / "extension_window_classify_test.go"
tt = testp.read_text(encoding="utf-8")
if "func TestAutoOpenedWalletHomepageTitle" not in tt:
    testp.write_text(
        tt.rstrip()
        + """\n\nfunc TestAutoOpenedWalletHomepageTitle(t *testing.T) {\n\tif !isAutoOpenedWalletHomepageTitle(\"Rabby Wallet Notification\") {\n\t\tt.Fatal(\"Rabby Wallet Notification is the start-path homepage host\")\n\t}\n\tif !isAutoOpenedWalletHomepageTitle(\"MetaMask Notification\") {\n\t\tt.Fatal(\"MetaMask Notification is the start-path homepage host\")\n\t}\n\tif isAutoOpenedWalletHomepageTitle(\"Rabby Wallet\") {\n\t\tt.Fatal(\"toolbar wallet popup must stay open\")\n\t}\n\tif isAutoOpenedWalletHomepageTitle(\"MetaMask\") {\n\t\tt.Fatal(\"toolbar MetaMask popup must stay open\")\n\t}\n\tif isAutoOpenedWalletHomepageTitle(\"Aura Protocol\") {\n\t\tt.Fatal(\"dapp tab must not match\")\n\t}\n\tif isAutoOpenedWalletHomepageTitle(\"\") {\n\t\tt.Fatal(\"empty title must not match\")\n\t}\n}\n""",
        encoding="utf-8",
    )

for rel in ("wails.json", "frontend/package.json", "frontend/package-lock.json"):
    p = root / rel
    p.write_text(p.read_text(encoding="utf-8").replace("1.7.132", "1.7.133"), encoding="utf-8")

pkg = root / "scripts" / "test_packaging_scripts.py"
pt = pkg.read_text(encoding="utf-8")
if "applyProfileNativeExtensionLaunchArgs(args, userDataDir)" not in pt:
    pt = pt.replace(
        '        self.assertIn("preparePrimaryEnvironmentLaunchArgs(args)", launch)\n',
        '        self.assertIn("preparePrimaryEnvironmentLaunchArgs(args)", launch)\n'
        '        self.assertIn("applyProfileNativeExtensionLaunchArgs(args, userDataDir)", launch)\n'
        '        self.assertIn("dismissStartupWalletNotificationHosts(profile.Pid)", launch)\n',
        1,
    )
    pt = pt.replace(
        '        self.assertNotIn("time.Sleep", startup_bounds)\n',
        '        self.assertNotIn("time.Sleep", startup_bounds)\n'
        '        self.assertIn("dismissStartupWalletNotificationHosts", self.read("backend/startup_wallet_notify_windows.go"))\n',
        1,
    )
    pt = pt.replace(
        '        self.assertNotIn("closeAssignedExtensionAutoPagesAfterStart", startup_tabs)\n'
        '        self.assertNotIn("finalizeBrowserStartupTabs", launch)\n',
        '        self.assertNotIn("closeAssignedExtensionAutoPagesAfterStart", startup_tabs)\n'
        '        self.assertIn("dismissStartupWalletNotificationHosts", startup_tabs)\n'
        '        self.assertIn("applyProfileNativeExtensionLaunchArgs", startup_tabs)\n'
        '        self.assertNotIn("finalizeBrowserStartupTabs", launch)\n',
        1,
    )
    pkg.write_text(pt, encoding="utf-8")

checks = {
    "backend/app_instance.go": [
        "applyProfileNativeExtensionLaunchArgs(args, userDataDir)",
        "dismissStartupWalletNotificationHosts(profile.Pid)",
    ],
    "backend/extension_window_classify.go": ["func isAutoOpenedWalletHomepageTitle"],
    "wails.json": ['"productVersion": "1.7.133"'],
}
for rel, needles in checks.items():
    body = (root / rel).read_text(encoding="utf-8")
    for n in needles:
        if n not in body:
            raise SystemExit("check failed %s %s" % (rel, n))
print("1.7.133 splice OK")
