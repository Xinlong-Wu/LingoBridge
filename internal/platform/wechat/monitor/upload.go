package monitor

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"wechatbox/internal/llm"
	"wechatbox/internal/platform/wechat/api"
	"wechatbox/internal/platform/wechat/cdn"
)

const defaultWeixinCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

type imageUploader func(client wechatClient, httpClient *http.Client, cdnBaseURL, toUserID string, image llm.Image) (*uploadedImage, error)

type uploadedImage struct {
	Media   *api.CDNMedia
	MidSize int
}

func uploadImageToWeixinCDN(client wechatClient, httpClient *http.Client, cdnBaseURL, toUserID string, image llm.Image) (*uploadedImage, error) {
	if len(image.Data) == 0 {
		return nil, fmt.Errorf("image data is empty")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	fileKey, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("generate filekey: %w", err)
	}

	aesKey := make([]byte, 16)
	if _, err := rand.Read(aesKey); err != nil {
		return nil, fmt.Errorf("generate aes key: %w", err)
	}
	aesKeyHex := hex.EncodeToString(aesKey)

	sum := md5.Sum(image.Data)
	ciphertextSize := cdn.AESPaddedSize(len(image.Data))
	uploadResp, err := client.GetUploadUrl(&api.GetUploadUrlReq{
		FileKey:     fileKey,
		MediaType:   api.UploadMediaTypeImage,
		ToUserID:    toUserID,
		RawSize:     len(image.Data),
		RawFileMD5:  hex.EncodeToString(sum[:]),
		FileSize:    ciphertextSize,
		NoNeedThumb: true,
		AESKey:      aesKeyHex,
	})
	if err != nil {
		return nil, fmt.Errorf("get upload url: %w", err)
	}

	ciphertext, err := cdn.EncryptAESECB(image.Data, aesKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt image: %w", err)
	}

	uploadURL, err := resolveCDNUploadURL(uploadResp, cdnBaseURL, fileKey)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, uploadURL, bytes.NewReader(ciphertext))
	if err != nil {
		return nil, fmt.Errorf("create CDN upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CDN upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("CDN upload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	downloadParam := resp.Header.Get("x-encrypted-param")
	if downloadParam == "" {
		return nil, fmt.Errorf("CDN upload response missing x-encrypted-param header")
	}

	return &uploadedImage{
		Media: &api.CDNMedia{
			EncryptQueryParam: downloadParam,
			AESKey:            encodeMediaAESKey(aesKeyHex),
			EncryptType:       1,
		},
		MidSize: len(ciphertext),
	}, nil
}

func resolveCDNUploadURL(uploadResp *api.GetUploadUrlResp, cdnBaseURL, fileKey string) (string, error) {
	if uploadResp == nil {
		return "", fmt.Errorf("get upload url returned nil response")
	}
	if uploadFullURL := strings.TrimSpace(uploadResp.UploadFullURL); uploadFullURL != "" {
		return uploadFullURL, nil
	}
	if uploadResp.UploadParam == "" {
		return "", fmt.Errorf("get upload url returned no upload_full_url or upload_param")
	}
	if cdnBaseURL == "" {
		return "", fmt.Errorf("CDN base URL is required when upload_full_url is missing")
	}

	base := strings.TrimRight(cdnBaseURL, "/")
	v := url.Values{}
	v.Set("encrypted_query_param", uploadResp.UploadParam)
	v.Set("filekey", fileKey)
	return base + "/upload?" + v.Encode(), nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func encodeMediaAESKey(hexKey string) string {
	return base64.StdEncoding.EncodeToString([]byte(hexKey))
}
