package login

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"lingobridge/internal/logging"
	"lingobridge/internal/platform/wechat/api"
	"lingobridge/internal/store"
)

const (
	fixedBaseURL      = "https://ilinkai.weixin.qq.com"
	defaultBotType    = "3"
	maxQRRefreshCount = 3
	loginTimeout      = 5 * time.Minute
	qrPollInterval    = 1 * time.Second
)

var loginLog = logging.For("wechat-login")

// Login performs the QR code login flow and saves the account.
func Login(st *store.Store, accountName string) error {
	ctx := context.Background()
	client := api.NewClient(fixedBaseURL, "")
	client.Debug = false

	// Get local token list
	accounts, _ := st.ListAccounts()
	var tokens []string
	for i := len(accounts) - 1; i >= 0 && len(tokens) < 10; i-- {
		tokens = append(tokens, accounts[i].Token)
	}

	// Step 1: Fetch QR code
	fmt.Println("📱 Fetching QR code...")
	qrResp, err := client.FetchQRCode(defaultBotType, tokens)
	if err != nil {
		return fmt.Errorf("fetch QR code: %w", err)
	}

	// Display QR code in terminal
	fmt.Println("\n用手机微信扫描以下二维码以继续连接：")
	qr, err := qrcode.New(qrResp.QRCodeImgContent, qrcode.Medium)
	if err != nil {
		fmt.Printf("无法生成二维码图像，请访问链接：%s\n", qrResp.QRCodeImgContent)
	} else {
		fmt.Println(qr.ToSmallString(false))
		fmt.Printf("\n若二维码无法显示，请访问：%s\n", qrResp.QRCodeImgContent)
	}

	// Step 2: Poll status
	fmt.Println("\n⏳ 等待扫码...")
	deadline := time.Now().Add(loginTimeout)
	scannedPrinted := false
	refreshCount := 1
	qrcode := qrResp.QRCode
	var pendingVerifyCode string
	pollBaseURL := fixedBaseURL
	botType := defaultBotType

	for time.Now().Before(deadline) {
		statusResp, err := client.PollQRStatus(qrcode, pendingVerifyCode)
		if err != nil {
			loginLog.Warn(ctx, "poll error: %v", err)
			time.Sleep(qrPollInterval)
			continue
		}

		switch statusResp.Status {
		case "wait":
			fmt.Print(".")

		case "scaned":
			if pendingVerifyCode != "" {
				pendingVerifyCode = ""
			}
			if !scannedPrinted {
				fmt.Println("\n✅ 已扫描，正在确认...")
				scannedPrinted = true
			}

		case "need_verifycode":
			prompt := "输入手机微信显示的数字，以继续连接："
			if pendingVerifyCode != "" {
				prompt = "❌ 你输入的数字不匹配，请重新输入："
			}
			fmt.Print(prompt)
			reader := bufio.NewReader(os.Stdin)
			code, _ := reader.ReadString('\n')
			pendingVerifyCode = strings.TrimSpace(code)
			continue

		case "verify_code_blocked":
			fmt.Println("\n⛔ 多次输入错误，正在刷新二维码...")
			pendingVerifyCode = ""
			refreshCount++
			if refreshCount > maxQRRefreshCount {
				return fmt.Errorf("多次输入错误，连接流程已停止")
			}
			newQR, err := refreshQR(client, botType, tokens, refreshCount)
			if err != nil {
				return err
			}
			qrcode = newQR.QRCode
			scannedPrinted = false

		case "expired":
			refreshCount++
			if refreshCount > maxQRRefreshCount {
				return fmt.Errorf("二维码多次失效，连接流程已停止")
			}
			fmt.Printf("\n⏳ 二维码已过期，正在刷新 (%d/%d)...\n", refreshCount, maxQRRefreshCount)
			newQR, err := refreshQR(client, botType, tokens, refreshCount)
			if err != nil {
				return err
			}
			qrcode = newQR.QRCode
			scannedPrinted = false

		case "binded_redirect":
			fmt.Println("\n✅ 已连接过此 LingoBridge，无需重复连接。")
			return nil

		case "scaned_but_redirect":
			if statusResp.RedirectHost != "" {
				pollBaseURL = "https://" + statusResp.RedirectHost
				client.BaseURL = pollBaseURL
				loginLog.Info(ctx, "IDC redirect: %s", statusResp.RedirectHost)
			}

		case "confirmed":
			if statusResp.ILinkBotID == "" {
				return fmt.Errorf("登录确认但未返回 ilink_bot_id")
			}

			// Save account
			baseURL := statusResp.BaseURL
			if baseURL == "" {
				baseURL = fixedBaseURL
			}

			acc := store.Account{
				ID:              statusResp.ILinkBotID,
				Name:            accountName,
				Platform:        store.PlatformWeChat,
				Token:           statusResp.BotToken,
				BaseURL:         baseURL,
				UserID:          statusResp.ILinkUserID,
				CredentialsJSON: "{}",
				Enabled:         true,
			}

			if err := st.SaveAccount(acc); err != nil {
				return fmt.Errorf("save account: %w", err)
			}

			fmt.Printf("\n✅ 已连接！账户: %s (%s)\n", accountName, statusResp.ILinkBotID)
			return nil
		}

		time.Sleep(qrPollInterval)
	}

	return fmt.Errorf("登录超时，请重试")
}

func refreshQR(client *api.Client, botType string, tokens []string, count int) (*api.QRCodeResponse, error) {
	fmt.Printf("🔄 正在刷新二维码...(%d/%d)\n", count, maxQRRefreshCount)
	qrResp, err := client.FetchQRCode(botType, tokens)
	if err != nil {
		return nil, fmt.Errorf("刷新二维码失败: %w", err)
	}

	fmt.Println("🔄 二维码已更新，请重新扫描。")
	qr, err := qrcode.New(qrResp.QRCodeImgContent, qrcode.Medium)
	if err == nil {
		fmt.Println(qr.ToSmallString(false))
	}
	return qrResp, nil
}
