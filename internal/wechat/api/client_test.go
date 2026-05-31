package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testHTTPClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func testResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestSendMessageSetsHeadersAndBaseInfo(t *testing.T) {
	client := NewClient("https://wechatbox.test", "token")
	client.HTTPClient = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/ilink/bot/sendmessage" {
			t.Fatalf("path = %s, want /ilink/bot/sendmessage", r.URL.Path)
		}
		if got := r.Header.Get("AuthorizationType"); got != "ilink_bot_token" {
			t.Fatalf("AuthorizationType = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-WECHAT-UIN"); got == "" {
			t.Fatal("X-WECHAT-UIN header is empty")
		}

		var req SendMessageReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.BaseInfo == nil {
			t.Fatal("BaseInfo is nil")
		}

		return testResponse(r, http.StatusOK, `{}`), nil
	})

	if err := client.SendMessage(&WeixinMessage{}); err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
}

func TestDoRequestTimeout(t *testing.T) {
	client := NewClient("https://wechatbox.test", "")
	client.HTTPClient = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	_, err := client.doGet("slow", 5*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("doGet error = %v, want ErrTimeout", err)
	}
}

func TestDoRequestHTTPError(t *testing.T) {
	client := NewClient("https://wechatbox.test", "")
	client.HTTPClient = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		return testResponse(r, http.StatusInternalServerError, "bad"), nil
	})

	_, err := client.doGet("bad", time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("doGet error = %v, want HTTP 500", err)
	}
}

func TestGetConfigInvalidJSON(t *testing.T) {
	client := NewClient("https://wechatbox.test", "")
	client.HTTPClient = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		return testResponse(r, http.StatusOK, `{not-json`), nil
	})

	_, err := client.GetConfig("user", "context")
	if err == nil || !strings.Contains(err.Error(), "unmarshal response") {
		t.Fatalf("GetConfig error = %v, want unmarshal response", err)
	}
}
