# Finish 1.7.133: hot-start trusts integrity marker, no re-inject, no extension CDP.
# Does not touch Cookies, Preferences, LES, wallets.
from pathlib import Path

root = Path(".")


def require(path: Path, *needles: str):
    text = path.read_text(encoding="utf-8")
    for n in needles:
        if n not in text:
            raise SystemExit("missing %r in %s" % (n, path))


inst = root / "backend" / "app_instance.go"
text = inst.read_text(encoding="utf-8")
if "applyCompleteExtensionLaunchArgs(args, userDataDir)" not in text:
    old = (
        "\targs = appendChromeTestingInfobarSuppressArg(args, isCloakSelectedCore)\n"
        "\t// One bounded fallback for an already present profile package. Keyless\n"
        "\t// profile packages are rejected; signed managed packages above are the\n"
        "\t// authoritative same-ID recovery path.\n"
        "\trecoveryProfileDir := chromeLaunchProfileDirectory(userDataDir, args)\n"
        "\targs, recoveredUserExtensions := appendProfileExtensionRecoveryLaunchArgs(args, userDataDir)\n"
        "\targs = normalizeLoadExtensionArgs(args)\n"
    )
    new = (
        "\targs = appendChromeTestingInfobarSuppressArg(args, isCloakSelectedCore)\n"
        "\tassignedLaunchArgs := append([]string(nil), args...)\n"
        "\tassignmentFingerprint, assignmentIDs := assignmentFingerprintFromLaunchArgs(assignedLaunchArgs)\n"
        "\targs, extensionsSettled := applyCompleteExtensionLaunchArgs(args, userDataDir)\n"
        "\tif extensionsSettled {\n"
        "\t\tlog.Info(\"\u6269\u5c55\u5df2\u5b8c\u6210\u9996\u6b21\u9002\u914d\uff0c\u542f\u52a8\u8df3\u8fc7\u6ce8\u5165/\u626b\u63cf/\u6062\u590d\uff08\u4e0d\u8bfb Preferences/LES/\u94b1\u5305\uff09\",\n"
        "\t\t\tlogger.F(\"profile_id\", profileId),\n"
        "\t\t\tlogger.F(\"assignment_ids\", strings.Join(assignmentIDs, \",\")),\n"
        "\t\t)\n"
        "\t} else {\n"
        "\t\trecoveryProfileDir := chromeLaunchProfileDirectory(userDataDir, args)\n"
        "\t\tvar recoveredUserExtensions int\n"
        "\t\targs, recoveredUserExtensions = appendProfileExtensionRecoveryLaunchArgs(args, userDataDir)\n"
        "\t\targs = normalizeLoadExtensionArgs(args)\n"
        "\t\targs, nativePresent, nativeNeedCLI := applyProfileNativeExtensionLaunchArgs(args, userDataDir)\n"
        "\t\targs = dropStoreInstalledLoadExtensionArgs(args, userDataDir)\n"
        "\t\tif nativePresent > 0 || nativeNeedCLI > 0 || recoveredUserExtensions > 0 {\n"
        "\t\t\tlog.Info(\"\u9996\u6b21\u9002\u914d\u6269\u5c55\u52a0\u8f7d\uff08\u53ea\u8bfb\uff0c\u4e0d\u6539\u5199\u7528\u6237\u6570\u636e\uff09\",\n"
        "\t\t\t\tlogger.F(\"profile_id\", profileId),\n"
        "\t\t\t\tlogger.F(\"chrome_profile_directory\", recoveryProfileDir),\n"
        "\t\t\t\tlogger.F(\"native_present\", nativePresent),\n"
        "\t\t\t\tlogger.F(\"need_cli\", nativeNeedCLI),\n"
        "\t\t\t\tlogger.F(\"recovered_extensions\", recoveredUserExtensions),\n"
        "\t\t\t)\n"
        "\t\t}\n"
        "\t}\n"
    )
    if old not in text:
        raise SystemExit("instance recovery block missing (already spliced?)")
    text = text.replace(old, new, 1)

old_drop = (
    "\targs = normalizeLoadExtensionArgs(args)\n"
    "\t// \u7528\u6237\u5728\u6d4f\u89c8\u5668\u5546\u57ce\u91cc\u81ea\u884c\u5b89\u88c5\u7684\u6269\u5c55\uff08Preferences location=INTERNAL\uff09\u7531\n"
    "\t// Chrome \u539f\u751f\u52a0\u8f7d\uff0c\u7981\u6b62\u518d\u901a\u8fc7 --load-extension \u6ce8\u5165\u540c\u4e00 ID \u7684\u7ba1\u7406\u5305\uff1a\n"
    "\t// \u53cc\u91cd\u5b89\u88c5\u4f1a\u518d\u6b21\u89e6\u53d1 onInstalled\uff08Rabby/MetaMask \u6b22\u8fce/\u89e3\u9501\u5f39\u7a97\uff09\uff0c\u5e76\u53ef\u80fd\n"
    "\t// \u8986\u76d6\u7528\u6237\u81ea\u884c\u5b89\u88c5\u7684\u7248\u672c\u3002\u8fd9\u91cc\u53ea\u5254\u9664\u5546\u57ce\u91cc\u5df2\u539f\u751f\u5b89\u88c5\u7684\u6761\u76ee\uff0c\n"
    "\t// BrowserStudio \u81ea\u5df1\u5206\u914d\u7684 unpacked \u5305\u4e0d\u53d7\u5f71\u54cd\uff08\u4ecd\u662f\u6743\u5a01\u5206\u914d\u8bb0\u5f55\uff09\u3002\n"
    "\targs = dropStoreInstalledLoadExtensionArgs(args, userDataDir)\n"
    "\tuserLaunchExtensionCount := len(activeLoadExtensionDirs(sanitizedProfileLaunchArgs))\n"
    "\tif userLaunchExtensionCount > 0 {\n"
    "\t\tlog.Info(\"\u542f\u52a8\u65f6\u4fdd\u7559\u7528\u6237\u6307\u5b9a\u7684\u6269\u5c55\u542f\u52a8\u53c2\u6570\uff08\u4e0d\u6539\u5199\u7528\u6237\u6570\u636e\uff09\",\n"
)
new_drop = (
    "\targs = normalizeLoadExtensionArgs(args)\n"
    "\tuserLaunchExtensionCount := len(activeLoadExtensionDirs(sanitizedProfileLaunchArgs))\n"
    "\tif userLaunchExtensionCount > 0 && !extensionsSettled {\n"
    "\t\tlog.Info(\"\u9996\u6b21\u9002\u914d\u4ecd\u4fdd\u7559\u5206\u914d\u7684\u6269\u5c55\u542f\u52a8\u53c2\u6570\uff08\u4e0d\u6539\u5199\u7528\u6237\u6570\u636e\uff09\",\n"
)
if old_drop in text:
    text = text.replace(old_drop, new_drop, 1)

if "maybeMarkExtensionLaunchReady(profileId, userDataDir, assignmentFingerprint, assignedLaunchArgs, assignmentIDs)" not in text:
    old_mark = "\t\t\tmarkStartPrepDone(userDataDir)\n"
    new_mark = (
        "\t\t\tmarkStartPrepDone(userDataDir)\n"
        "\t\t\tif !extensionsSettled && assignmentFingerprint != \"\" {\n"
        "\t\t\t\tgo func() {\n"
        "\t\t\t\t\tdefer func() {\n"
        "\t\t\t\t\t\tif r := recover(); r != nil {\n"
        "\t\t\t\t\t\t\tlogger.New(\"Extension\").Error(\"maybeMarkExtensionLaunchReady panic recovered\",\n"
        "\t\t\t\t\t\t\t\tlogger.F(\"profile_id\", profileId),\n"
        "\t\t\t\t\t\t\t\tlogger.F(\"error\", r),\n"
        "\t\t\t\t\t\t\t)\n"
        "\t\t\t\t\t\t}\n"
        "\t\t\t\t\t}()\n"
        "\t\t\t\t\ta.maybeMarkExtensionLaunchReady(profileId, userDataDir, assignmentFingerprint, assignedLaunchArgs, assignmentIDs)\n"
        "\t\t\t\t}()\n"
        "\t\t\t}\n"
    )
    if old_mark not in text:
        raise SystemExit("markStartPrepDone missing")
    text = text.replace(old_mark, new_mark, 1)

text = text.replace(
    "\t\t\tgo dismissStartupWalletNotificationHosts(profile.Pid)\n",
    "",
)
inst.write_text(text, encoding="utf-8")

for rel in ("wails.json", "frontend/package.json", "frontend/package-lock.json"):
    p = root / rel
    body = p.read_text(encoding="utf-8")
    if "1.7.133" not in body:
        p.write_text(body.replace("1.7.132", "1.7.133"), encoding="utf-8")

require(
    inst,
    "applyCompleteExtensionLaunchArgs(args, userDataDir)",
    "maybeMarkExtensionLaunchReady(profileId, userDataDir, assignmentFingerprint, assignedLaunchArgs, assignmentIDs)",
)
require(root / "backend" / "extension_integrity.go", "func applyCompleteExtensionLaunchArgs")
require(root / "backend" / "navigation_guards.go", "if isInternalBrowserURL(raw)")
require(root / "wails.json", '"productVersion": "1.7.133"')
print("1.7.133 settled-extension splice OK")
