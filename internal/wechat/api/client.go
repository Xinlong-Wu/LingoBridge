package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultLongPollTimeout = 35 * time.Second
	defaultAPITimeout      = 15 * time.Second
	defaultConfigTimeout   = 10 * time.Second
)

// Client is the HTTP client for the WeChat Bot API.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Debug      bool
}

// NewClient creates a new WeChat API client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: defaultAPITimeout,
		},
	}
}

// randomWechatUin generates a random X-WECHAT-UIN header value.
func randomWechatUin() string {
	b := make([]byte, 4)
	rand.Read(b)
	uin := binary.BigEndian.Uint32(b)
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(uin), 10)))
}

// buildHeaders returns the common headers for API requests.
func (c *Client) buildHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("AuthorizationType", "ilink_bot_token")
	h.Set("X-WECHAT-UIN", randomWechatUin())
	if c.Token != "" {
		h.Set("Authorization", "Bearer "+c.Token)
	}
	return h
}

// getBaseInfo returns the base info payload.
func getBaseInfo() *BaseInfo {
	return &BaseInfo{
		ChannelVersion: "1.0.0",
		BotAgent:       "WeChatBox/1.0.0",
	}
}

// doPost sends a POST request and returns the response body.
func (c *Client) doPost(endpoint string, body interface{}, timeout time.Duration) ([]byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	reqURL := c.BaseURL + "/" + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header = c.buildHeaders()

	client := c.HTTPClient
	if timeout > 0 {
		client = &http.Client{Timeout: timeout}
	}

	if c.Debug {
		log.Printf("[wechat-api] POST %s body=%s", reqURL, truncate(string(bodyBytes), 500))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if c.Debug {
		log.Printf("[wechat-api] POST %s status=%d body=%s", reqURL, resp.StatusCode, truncate(string(respBody), 500))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	return respBody, nil
}

// doGet sends a GET request and returns the response body.
func (c *Client) doGet(endpoint string, timeout time.Duration) ([]byte, error) {
	reqURL := c.BaseURL + "/" + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	// GET uses common headers (without Authorization)
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTPClient
	if timeout > 0 {
		client = &http.Client{Timeout: timeout}
	}

	if c.Debug {
		log.Printf("[wechat-api] GET %s", reqURL)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if c.Debug {
		log.Printf("[wechat-api] GET %s status=%d body=%s", reqURL, resp.StatusCode, truncate(string(respBody), 500))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	return respBody, nil
}

// GetUpdates long-polls for new messages.
func (c *Client) GetUpdates(buf string) (*GetUpdatesResp, error) {
	req := GetUpdatesReq{
		GetUpdatesBuf: buf,
		BaseInfo:      getBaseInfo(),
	}

	respBody, err := c.doPost("ilink/bot/getupdates", req, defaultLongPollTimeout)
	if err != nil {
		// Return empty response on timeout (normal long-poll behavior)
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
			return &GetUpdatesResp{
				Ret:           0,
				Msgs:          []*WeixinMessage{},
				GetUpdatesBuf: buf,
			}, nil
		}
		return nil, err
	}

	var resp GetUpdatesResp
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &resp, nil
}

// SendMessage sends a message to WeChat.
func (c *Client) SendMessage(msg *WeixinMessage) error {
	req := SendMessageReq{
		Msg:      msg,
		BaseInfo: getBaseInfo(),
	}
	_, err := c.doPost("ilink/bot/sendmessage", req, defaultAPITimeout)
	return err
}

// GetUploadUrl gets a pre-signed CDN upload URL.
func (c *Client) GetUploadUrl(req *GetUploadUrlReq) (*GetUploadUrlResp, error) {
	req.BaseInfo = getBaseInfo()
	respBody, err := c.doPost("ilink/bot/getuploadurl", req, defaultAPITimeout)
	if err != nil {
		return nil, err
	}

	var resp GetUploadUrlResp
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &resp, nil
}

// GetConfig fetches bot configuration (including typing ticket).
func (c *Client) GetConfig(ilinkUserID, contextToken string) (*GetConfigResp, error) {
	body := map[string]interface{}{
		"ilink_user_id": ilinkUserID,
		"context_token": contextToken,
		"base_info":     getBaseInfo(),
	}
	respBody, err := c.doPost("ilink/bot/getconfig", body, defaultConfigTimeout)
	if err != nil {
		return nil, err
	}

	var resp GetConfigResp
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &resp, nil
}

// SendTyping sends a typing indicator.
func (c *Client) SendTyping(ilinkUserID, typingTicket string, status int) error {
	req := SendTypingReq{
		ILinkUserID:  ilinkUserID,
		TypingTicket: typingTicket,
		Status:       status,
		BaseInfo:     getBaseInfo(),
	}
	_, err := c.doPost("ilink/bot/sendtyping", req, defaultConfigTimeout)
	return err
}

// NotifyStart notifies the server of client startup.
func (c *Client) NotifyStart() error {
	req := NotifyStartReq{BaseInfo: getBaseInfo()}
	_, err := c.doPost("ilink/bot/msg/notifystart", req, defaultConfigTimeout)
	return err
}

// NotifyStop notifies the server of client shutdown.
func (c *Client) NotifyStop() error {
	req := NotifyStopReq{BaseInfo: getBaseInfo()}
	_, err := c.doPost("ilink/bot/msg/notifystop", req, defaultConfigTimeout)
	return err
}

// --- QR Code API ---

// QRCodeResponse is the response from get_bot_qrcode.
type QRCodeResponse struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

// QRStatusResponse is the response from get_qrcode_status.
type QRStatusResponse struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token,omitempty"`
	ILinkBotID   string `json:"ilink_bot_id,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	ILinkUserID  string `json:"ilink_user_id,omitempty"`
	RedirectHost string `json:"redirect_host,omitempty"`
}

// FetchQRCode fetches a QR code for login.
func (c *Client) FetchQRCode(botType string, localTokens []string) (*QRCodeResponse, error) {
	endpoint := fmt.Sprintf("ilink/bot/get_bot_qrcode?bot_type=%s", url.QueryEscape(botType))
	body := map[string]interface{}{
		"local_token_list": localTokens,
		"base_info":        getBaseInfo(),
	}
	respBody, err := c.doPost(endpoint, body, defaultConfigTimeout)
	if err != nil {
		return nil, err
	}

	var resp QRCodeResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal QR response: %w", err)
	}
	return &resp, nil
}

// PollQRStatus polls the QR code scan status.
func (c *Client) PollQRStatus(qrcode, verifyCode string) (*QRStatusResponse, error) {
	endpoint := fmt.Sprintf("ilink/bot/get_qrcode_status?qrcode=%s", url.QueryEscape(qrcode))
	if verifyCode != "" {
		endpoint += "&verify_code=" + url.QueryEscape(verifyCode)
	}

	respBody, err := c.doGet(endpoint, defaultLongPollTimeout)
	if err != nil {
		// Return wait on timeout
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
			return &QRStatusResponse{Status: "wait"}, nil
		}
		return &QRStatusResponse{Status: "wait"}, nil
	}

	var resp QRStatusResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal status response: %w", err)
	}
	return &resp, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
