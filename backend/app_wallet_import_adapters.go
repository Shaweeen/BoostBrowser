package backend

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ethereumAddressPattern = regexp.MustCompile(`(?i)0x[0-9a-f]{40}`)
	solanaAddressPattern   = regexp.MustCompile(`\b[1-9A-HJ-NP-Za-km-z]{32,44}\b`)
)

func importMnemonicIntoFreshWallet(walletType string, debugPort int, mnemonic, password string) (string, error) {
	switch walletType {
	case "rabby":
		return importMnemonicIntoFreshRabby(debugPort, mnemonic, password)
	case "jupiter":
		return importMnemonicIntoFreshJupiter(debugPort, mnemonic, password)
	case "metamask":
		return importMnemonicIntoFreshMetaMask(debugPort, mnemonic, password)
	default:
		return "", fmt.Errorf("不支持的钱包类型")
	}
}

func openWalletImportTarget(debugPort int, url, walletName string) (*rabbyCDPClient, *rabbyCDPClient, string, error) {
	browserWS, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return nil, nil, "", err
	}
	browserClient, err := newRabbyCDPClient(browserWS)
	if err != nil {
		return nil, nil, "", fmt.Errorf("连接浏览器调试接口失败：%w", err)
	}
	created, err := browserClient.call("Target.createTarget", map[string]any{"url": url}, 8*time.Second)
	if err != nil {
		browserClient.close()
		return nil, nil, "", fmt.Errorf("打开 %s 导入页面失败：%w", walletName, err)
	}
	targetID, _ := created["targetId"].(string)
	if targetID == "" {
		browserClient.close()
		return nil, nil, "", fmt.Errorf("%s 导入页面未创建", walletName)
	}
	target, err := waitForRabbyTarget(debugPort, targetID, 15*time.Second)
	if err != nil {
		_, _ = browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, 3*time.Second)
		browserClient.close()
		return nil, nil, "", fmt.Errorf("等待 %s 扩展页面超时", walletName)
	}
	pageClient, err := newRabbyCDPClient(target.WebSocketDebuggerUrl)
	if err != nil {
		_, _ = browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, 3*time.Second)
		browserClient.close()
		return nil, nil, "", fmt.Errorf("连接 %s 页面失败：%w", walletName, err)
	}
	return browserClient, pageClient, targetID, nil
}

func closeWalletImportTarget(browserClient, pageClient *rabbyCDPClient, targetID string) {
	if pageClient != nil {
		pageClient.close()
	}
	if browserClient != nil {
		if targetID != "" {
			_, _ = browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, 3*time.Second)
		}
		browserClient.close()
	}
}

func focusAndInsertWalletText(client *rabbyCDPClient, selector, value, fieldName string) error {
	expression := fmt.Sprintf(`(() => { const input = document.querySelector(%q); if (!input || input.disabled) return false; input.focus(); return true; })()`, selector)
	focused, err := client.evaluate(expression)
	if err != nil || focused != true {
		return fmt.Errorf("无法定位%s", fieldName)
	}
	if _, err := client.call("Input.insertText", map[string]any{"text": value}, 8*time.Second); err != nil {
		return fmt.Errorf("写入%s失败", fieldName)
	}
	return nil
}

func clickWalletElement(client *rabbyCDPClient, selector, label string) error {
	expression := fmt.Sprintf(`(() => { const el = document.querySelector(%q); if (!el || el.disabled) return false; el.click(); return true; })()`, selector)
	clicked, err := client.evaluate(expression)
	if err != nil || clicked != true {
		return fmt.Errorf("%s不可用", label)
	}
	return nil
}

func clickWalletButtonByText(client *rabbyCDPClient, label string) error {
	expression := fmt.Sprintf(`(() => { const wanted = %q; const button = [...document.querySelectorAll('button')].find((item) => item.innerText.trim() === wanted && !item.disabled); if (!button) return false; button.click(); return true; })()`, label)
	clicked, err := client.evaluate(expression)
	if err != nil || clicked != true {
		return fmt.Errorf("%s按钮不可用", label)
	}
	return nil
}

func importMnemonicIntoFreshJupiter(debugPort int, mnemonic, password string) (string, error) {
	url := "chrome-extension://" + jupiterExtensionID + "/popup.html#/onboard"
	browserClient, pageClient, targetID, err := openWalletImportTarget(debugPort, url, "Jupiter")
	if err != nil {
		return "", err
	}
	defer closeWalletImportTarget(browserClient, pageClient, targetID)

	freshSelector := `location.hash.includes('/onboard') && document.body.innerText.includes('Welcome to Jupiter Wallet') && [...document.querySelectorAll('button')].some((item) => item.innerText.trim() === 'Import Existing Wallet')`
	if err := waitRabbyCondition(pageClient, freshSelector, 18*time.Second); err != nil {
		return "", fmt.Errorf("Jupiter 已初始化或扩展版本不兼容，已拒绝覆盖")
	}
	if err := clickWalletButtonByText(pageClient, "Import Existing Wallet"); err != nil {
		return "", err
	}
	if err := waitRabbyCondition(pageClient, `[...document.querySelectorAll('button')].some((item) => item.innerText.trim() === 'Seed Phrase')`, 10*time.Second); err != nil {
		return "", fmt.Errorf("Jupiter 导入方式页面加载失败")
	}
	if err := clickWalletButtonByText(pageClient, "Seed Phrase"); err != nil {
		return "", err
	}
	if err := waitRabbyCondition(pageClient, `document.querySelector('textarea[name="textarea-input"]') !== null && document.body.innerText.includes('Import Seed Phrase')`, 12*time.Second); err != nil {
		return "", fmt.Errorf("Jupiter 助记词页面加载失败")
	}
	if err := focusAndInsertWalletText(pageClient, `textarea[name="textarea-input"]`, mnemonic, "Jupiter 助记词"); err != nil {
		return "", err
	}
	time.Sleep(500 * time.Millisecond)
	if err := clickWalletButtonByText(pageClient, "Next"); err != nil {
		return "", fmt.Errorf("Jupiter 未接受助记词，请检查文件内容")
	}
	if err := waitRabbyCondition(pageClient, `document.body.innerText.includes('Confirm Addresses')`, 30*time.Second); err != nil {
		return "", fmt.Errorf("Jupiter 地址确认页面加载超时")
	}
	addressValue, _ := pageClient.evaluate(`(() => { const match = document.body.innerText.match(/\b[1-9A-HJ-NP-Za-km-z]{32,44}\b/); return match ? match[0] : ''; })()`)
	address, _ := addressValue.(string)
	if err := clickWalletButtonByText(pageClient, "Next"); err != nil {
		return "", fmt.Errorf("Jupiter 地址确认按钮不可用")
	}
	if err := waitRabbyCondition(pageClient, `document.body.innerText.includes('Label Your Wallet') && document.querySelector('input[name="password-input"]') !== null`, 15*time.Second); err != nil {
		return "", fmt.Errorf("Jupiter 密码设置页面加载失败")
	}
	if err := focusAndInsertWalletText(pageClient, `input[name="wallet-name-input"]`, "Wallet", "Jupiter 钱包名称"); err != nil {
		return "", err
	}
	if err := focusAndInsertWalletText(pageClient, `input[name="password-input"]`, password, "Jupiter 本地密码"); err != nil {
		return "", err
	}
	if err := focusAndInsertWalletText(pageClient, `input[name="confirm-password-input"]`, password, "Jupiter 确认密码"); err != nil {
		return "", err
	}
	time.Sleep(400 * time.Millisecond)
	if err := clickWalletButtonByText(pageClient, "Next"); err != nil {
		return "", fmt.Errorf("Jupiter 密码确认按钮不可用")
	}
	state, err := waitRabbyValue(pageClient, `(() => { const text = document.body.innerText; if (text.includes('Recommended Settings')) return 'settings'; if (text.includes('Continue to jup.ag to start')) return 'complete'; return ''; })()`, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("Jupiter 创建保险库超时")
	}
	if state == "settings" {
		if err := clickWalletButtonByText(pageClient, "Get Started"); err != nil {
			return "", err
		}
		if err := waitRabbyCondition(pageClient, `document.body.innerText.includes('Continue to jup.ag to start')`, 20*time.Second); err != nil {
			return "", fmt.Errorf("Jupiter 完成页面加载超时")
		}
	}
	if address != "" && !solanaAddressPattern.MatchString(address) {
		address = ""
	}
	return address, nil
}

func importMnemonicIntoFreshMetaMask(debugPort int, mnemonic, password string) (string, error) {
	url := "chrome-extension://" + metamaskExtensionID + "/home.html#onboarding/welcome"
	browserClient, pageClient, targetID, err := openWalletImportTarget(debugPort, url, "MetaMask")
	if err != nil {
		return "", err
	}
	defer closeWalletImportTarget(browserClient, pageClient, targetID)

	freshSelector := `document.querySelector('[data-testid="onboarding-create-wallet"]') !== null && document.querySelector('[data-testid="onboarding-import-wallet"]') !== null`
	if err := waitRabbyCondition(pageClient, freshSelector, 20*time.Second); err != nil {
		return "", fmt.Errorf("MetaMask 已初始化或扩展版本不兼容，已拒绝覆盖")
	}
	if err := clickWalletElement(pageClient, `[data-testid="onboarding-import-wallet"]`, "MetaMask 导入钱包按钮"); err != nil {
		return "", err
	}
	if err := waitRabbyCondition(pageClient, `document.querySelector('[data-testid="onboarding-import-with-srp-button"]') !== null`, 12*time.Second); err != nil {
		return "", fmt.Errorf("MetaMask 导入方式页面加载失败")
	}
	if err := clickWalletElement(pageClient, `[data-testid="onboarding-import-with-srp-button"]`, "MetaMask 助记词导入按钮"); err != nil {
		return "", err
	}
	if err := waitRabbyCondition(pageClient, `document.querySelector('[data-testid="srp-input-import__srp-note"]') !== null`, 12*time.Second); err != nil {
		return "", fmt.Errorf("MetaMask 助记词页面加载失败")
	}
	if err := focusAndInsertWalletText(pageClient, `[data-testid="srp-input-import__srp-note"]`, mnemonic, "MetaMask 助记词"); err != nil {
		return "", err
	}
	if err := waitRabbyCondition(pageClient, `(() => { const button = document.querySelector('[data-testid="import-srp-confirm"]'); return Boolean(button && !button.disabled); })()`, 8*time.Second); err != nil {
		return "", fmt.Errorf("MetaMask 未接受助记词，请检查文件内容")
	}
	if err := clickWalletElement(pageClient, `[data-testid="import-srp-confirm"]`, "MetaMask 助记词确认按钮"); err != nil {
		return "", err
	}
	if err := waitRabbyCondition(pageClient, `document.querySelector('[data-testid="create-password-new-input"]') !== null`, 15*time.Second); err != nil {
		return "", fmt.Errorf("MetaMask 密码设置页面加载失败")
	}
	if err := focusAndInsertWalletText(pageClient, `[data-testid="create-password-new-input"]`, password, "MetaMask 本地密码"); err != nil {
		return "", err
	}
	if err := focusAndInsertWalletText(pageClient, `[data-testid="create-password-confirm-input"]`, password, "MetaMask 确认密码"); err != nil {
		return "", err
	}
	checked, _ := pageClient.evaluate(`(() => { const checkbox = document.querySelector('[data-testid="create-password-terms"]'); if (!checkbox) return false; if (!checkbox.checked && checkbox.getAttribute('data-checked') !== 'true') checkbox.click(); return true; })()`)
	if checked != true {
		return "", fmt.Errorf("MetaMask 使用条款确认框不可用")
	}
	if err := clickWalletElement(pageClient, `[data-testid="create-password-submit"]`, "MetaMask 创建密码按钮"); err != nil {
		return "", err
	}

	for attempts := 0; attempts < 4; attempts++ {
		stateValue, waitErr := waitRabbyValue(pageClient, `(() => { if (document.querySelector('[data-testid="passkey-maybe-later-button"]')) return 'passkey'; if (document.querySelector('[data-testid="metametrics-i-agree"]')) return 'metrics'; if (document.querySelector('[data-testid="onboarding-complete-done"]')) return 'complete'; return ''; })()`, 20*time.Second)
		if waitErr != nil {
			return "", fmt.Errorf("MetaMask 初始化流程超时")
		}
		state, _ := stateValue.(string)
		switch state {
		case "passkey":
			if err := clickWalletElement(pageClient, `[data-testid="passkey-maybe-later-button"]`, "MetaMask 跳过通行密钥按钮"); err != nil {
				return "", err
			}
		case "metrics":
			_, _ = pageClient.evaluate(`(() => { const checkbox = document.querySelector('[data-testid="metametrics-checkbox"]'); if (checkbox && checkbox.getAttribute('data-checked') === 'true') checkbox.click(); return true; })()`)
			if err := clickWalletElement(pageClient, `[data-testid="metametrics-i-agree"]`, "MetaMask 继续按钮"); err != nil {
				return "", err
			}
		case "complete":
			addressValue, _ := pageClient.evaluate(`(() => { const match = document.body.innerText.match(/0x[0-9a-fA-F]{40}/); return match ? match[0] : ''; })()`)
			address, _ := addressValue.(string)
			if address != "" && !ethereumAddressPattern.MatchString(address) {
				address = ""
			}
			return address, nil
		}
	}
	return "", fmt.Errorf("MetaMask 完成页面加载超时")
}

func sanitizeWalletPublicAddress(value string) string {
	value = strings.TrimSpace(value)
	if ethereumAddressPattern.MatchString(value) || solanaAddressPattern.MatchString(value) {
		return value
	}
	return ""
}
