# Compile-only 1.7.133 repair. Does not touch Cookies / Preferences / LES / wallets.
from pathlib import Path

inst = Path("backend/app_instance.go")
text = inst.read_text(encoding="utf-8")
needle = "\tif recoveredUserExtensions > 0 {\n"
idx = text.find(needle)
if idx < 0:
    print("recover leftover already gone")
else:
    end = text.find("\t}\n", idx)
    if end < 0:
        raise SystemExit("leftover recover log has no end")
    inst.write_text(text[:idx] + text[end + 3 :], encoding="utf-8")
    print("removed leftover recover log")

classify = Path("backend/extension_window_classify.go")
ct = classify.read_text(encoding="utf-8")
if "func isAutoOpenedWalletHomepageTitle" not in ct:
    marker = "\treturn w >= 200 && h >= 100\n}\n"
    fn = (
        "\treturn w >= 200 && h >= 100\n}\n\n"
        "// isAutoOpenedWalletHomepageTitle matches MV3 wallet Notification hosts.\n"
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
    print("added wallet title helper")
else:
    print("wallet title helper already present")

inst2 = inst.read_text(encoding="utf-8")
if "if recoveredUserExtensions > 0" in inst2:
    raise SystemExit("recover leftover still present")
if "func isAutoOpenedWalletHomepageTitle" not in classify.read_text(encoding="utf-8"):
    raise SystemExit("title helper missing")
print("compile splice OK")
