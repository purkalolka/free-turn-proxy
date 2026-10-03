package vkauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
)

type mockRequesterClient struct {
	server *httptest.Server
}

func (m *mockRequesterClient) doMockRequest(ctx context.Context, _ tlsclient.HttpClient, _ browserprofile.Profile, data, rawURL string) (map[string]any, error) {
	targetURL := m.server.URL
	if idx := strings.Index(rawURL, "?"); idx != -1 {
		targetURL += rawURL[idx:]
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.server.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var res map[string]any
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func TestVKCallsParsingAndURLParams(t *testing.T) {
	t.Parallel()

	var (
		step1Called atomic.Bool
		step2Called atomic.Bool
		step3Called atomic.Bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()

		// Distinguish by method query param or presence in URL
		rawQuery := r.URL.RawQuery

		switch {
		case strings.Contains(r.URL.Path, "auth.getAnonymToken") || strings.Contains(rawQuery, "anonymName="):
			step1Called.Store(true)
			// Check required query parameters
			if q.Get("v") != vkCallsAPIVersion {
				t.Errorf("step 1: expected v=%s, got %s", vkCallsAPIVersion, q.Get("v"))
			}
			if q.Get("client_id") != vkCallsClientID {
				t.Errorf("step 1: expected client_id=%s, got %s", vkCallsClientID, q.Get("client_id"))
			}
			if !strings.HasPrefix(q.Get("link"), "https://vk.com/call/join/") {
				t.Errorf("step 1: link param must start with https://vk.com/call/join/, got %s", q.Get("link"))
			}
			if q.Get("device_id") == "" {
				t.Errorf("step 1: device_id is empty")
			}
			if q.Get("anonymName") == "" {
				t.Errorf("step 1: anonymName is empty")
			}
			if q.Get("lang") != "en" {
				t.Errorf("step 1: expected lang=en, got %s", q.Get("lang"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{
					"anonymous_token": "anon-token-123",
				},
			})

		case strings.Contains(r.URL.Path, "messages.getCallPreview") || strings.Contains(rawQuery, "extended=1"):
			step2Called.Store(true)
			if q.Get("v") != vkCallsAPIVersion {
				t.Errorf("step 2: expected v=%s, got %s", vkCallsAPIVersion, q.Get("v"))
			}
			if q.Get("client_id") != vkCallsClientID {
				t.Errorf("step 2: expected client_id=%s, got %s", vkCallsClientID, q.Get("client_id"))
			}
			if q.Get("anonymous_token") != "anon-token-123" {
				t.Errorf("step 2: expected anonymous_token=anon-token-123, got %s", q.Get("anonymous_token"))
			}
			if q.Get("extended") != "1" {
				t.Errorf("step 2: expected extended=1, got %s", q.Get("extended"))
			}
			if q.Get("fields") != "first_name,last_name,photo_200" {
				t.Errorf("step 2: expected fields=first_name,last_name,photo_200, got %s", q.Get("fields"))
			}
			if q.Get("lang") != "en" {
				t.Errorf("step 2: expected lang=en, got %s", q.Get("lang"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{
					"user_id": float64(987654),
					"secret":  "call-secret-xyz",
				},
			})

		case strings.Contains(r.URL.Path, "messages.getAnonymCallToken") || strings.Contains(rawQuery, "secret="):
			step3Called.Store(true)
			if q.Get("v") != vkCallsAPIVersion {
				t.Errorf("step 3: expected v=%s, got %s", vkCallsAPIVersion, q.Get("v"))
			}
			if q.Get("client_id") != vkCallsClientID {
				t.Errorf("step 3: expected client_id=%s, got %s", vkCallsClientID, q.Get("client_id"))
			}
			if q.Get("anonymous_token") != "anon-token-123" {
				t.Errorf("step 3: expected anonymous_token=anon-token-123, got %s", q.Get("anonymous_token"))
			}
			if q.Get("user_id") != "987654" {
				t.Errorf("step 3: expected user_id=987654, got %s", q.Get("user_id"))
			}
			if q.Get("secret") != "call-secret-xyz" {
				t.Errorf("step 3: expected secret=call-secret-xyz, got %s", q.Get("secret"))
			}
			if q.Get("name") == "" {
				t.Errorf("step 3: name is empty")
			}
			if q.Get("lang") != "en" {
				t.Errorf("step 3: expected lang=en, got %s", q.Get("lang"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{
					"token": "call-token-456",
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mock := &mockRequesterClient{server: server}
	client := New(Config{})
	client.requestFn = mock.doMockRequest
	profile := browserprofile.For(browserprofile.Desktop, browserprofile.Identity{Seed: "test", Gen: 1})

	// Test Step 1
	token1, err := client.fetchVKCallsAnonToken(context.Background(), nil, profile, "https://vk.com/call/join/testlink", "device-uuid-1", "John+Doe")
	if err != nil {
		t.Fatalf("fetchVKCallsAnonToken error: %v", err)
	}
	if token1 != "anon-token-123" {
		t.Fatalf("expected token1 anon-token-123, got %s", token1)
	}
	if !step1Called.Load() {
		t.Errorf("step 1 was not called")
	}

	// Test Step 2
	userID, secret, err := client.fetchVKCallsCallPreview(context.Background(), nil, profile, token1, "device-uuid-1", "https://vk.com/call/join/testlink")
	if err != nil {
		t.Fatalf("fetchVKCallsCallPreview error: %v", err)
	}
	if userID != "987654" || secret != "call-secret-xyz" {
		t.Fatalf("expected userID=987654 secret=call-secret-xyz, got %s / %s", userID, secret)
	}
	if !step2Called.Load() {
		t.Errorf("step 2 was not called")
	}

	// Test Step 3
	token2, err := client.fetchVKCallsAnonymCallToken(context.Background(), nil, profile, token1, "device-uuid-1", "https://vk.com/call/join/testlink", "John+Doe", userID, secret)
	if err != nil {
		t.Fatalf("fetchVKCallsAnonymCallToken error: %v", err)
	}
	if token2 != "call-token-456" {
		t.Fatalf("expected token2 call-token-456, got %s", token2)
	}
	if !step3Called.Load() {
		t.Errorf("step 3 was not called")
	}
}

func TestVKCallsErrorHandling(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		respJSON   string
		expectErr  error
		errContain string
	}{
		{
			name:      "invalid join link code 9000",
			respJSON:  `{"error":{"error_code":9000,"error_msg":"join link expired"}}`,
			expectErr: ErrInvalidJoinLink,
		},
		{
			name:      "anonym blocked message",
			respJSON:  `{"error":{"error_code":15,"error_msg":"Anonymous calls blocked"}}`,
			expectErr: ErrAnonymousBlocked,
		},
		{
			name:      "call full message",
			respJSON:  `{"error":{"error_code":15,"error_msg":"Call is full"}}`,
			expectErr: ErrCallFull,
		},
		{
			name:       "generic API error",
			respJSON:   `{"error":{"error_code":100,"error_msg":"One of the parameters specified was missing or invalid"}}`,
			errContain: "error_code:100",
		},
		{
			name:       "missing token in response",
			respJSON:   `{"response":{}}`,
			errContain: "missing anonymous_token",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.respJSON))
			}))
			defer server.Close()

			mock := &mockRequesterClient{server: server}
			client := New(Config{})
			client.requestFn = mock.doMockRequest
			profile := browserprofile.For(browserprofile.Desktop, browserprofile.Identity{Seed: "test", Gen: 1})

			_, err := client.fetchVKCallsAnonToken(context.Background(), nil, profile, "https://vk.com/call/join/test", "uuid", "name")
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if tc.expectErr != nil && !errors.Is(err, tc.expectErr) {
				t.Fatalf("expected error %v, got %v", tc.expectErr, err)
			}
			if tc.errContain != "" && !strings.Contains(err.Error(), tc.errContain) {
				t.Fatalf("expected error containing %q, got %v", tc.errContain, err)
			}
		})
	}
}

func TestClientFetchVKCallsChainFallback(t *testing.T) {
	t.Parallel()

	t.Run("success via vkcalls bypasses standard tokenChain", func(t *testing.T) {
		var (
			vkCallsCalled  atomic.Bool
			tokenChainHits atomic.Int32
		)

		c := newTestClient(t, func(_ context.Context, _ string, _ int, _ VKCredentials, _ tlsclient.CookieJar) (string, string, []string, error) {
			tokenChainHits.Add(1)
			return "std-u", "std-p", []string{"std:1"}, nil
		})
		c.vkCallsChain = func(ctx context.Context, link string, streamID int, jar tlsclient.CookieJar) (string, string, []string, error) {
			vkCallsCalled.Store(true)
			return "vkcalls-u", "vkcalls-p", []string{"vkcalls:3478"}, nil
		}

		u, p, addrs, err := c.GetCredentials(context.Background(), "call-id", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if u != "vkcalls-u" || p != "vkcalls-p" || addrs[0] != "vkcalls:3478" {
			t.Fatalf("unexpected creds: u=%s p=%s addrs=%v", u, p, addrs)
		}
		if !vkCallsCalled.Load() {
			t.Fatalf("expected vkCallsChain to be called")
		}
		if tokenChainHits.Load() != 0 {
			t.Fatalf("expected standard tokenChain to not be called on vkcalls success")
		}
	})

	t.Run("vkcalls failure falls back to standard tokenChain", func(t *testing.T) {
		var (
			vkCallsCalled  atomic.Bool
			tokenChainHits atomic.Int32
		)

		c := newTestClient(t, func(_ context.Context, _ string, _ int, creds VKCredentials, _ tlsclient.CookieJar) (string, string, []string, error) {
			tokenChainHits.Add(1)
			return "std-u", "std-p", []string{"std:1"}, nil
		})
		c.vkCallsChain = func(ctx context.Context, link string, streamID int, jar tlsclient.CookieJar) (string, string, []string, error) {
			vkCallsCalled.Store(true)
			return "", "", nil, errors.New("vk calls api 403 forbidden or rate limited")
		}

		u, p, addrs, err := c.GetCredentials(context.Background(), "call-id", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if u != "std-u" || p != "std-p" || addrs[0] != "std:1" {
			t.Fatalf("unexpected creds: u=%s p=%s addrs=%v", u, p, addrs)
		}
		if !vkCallsCalled.Load() {
			t.Fatalf("expected vkCallsChain to be called")
		}
		if tokenChainHits.Load() != 1 {
			t.Fatalf("expected standard tokenChain to be called 1 time on fallback, got %d", tokenChainHits.Load())
		}
	})

	t.Run("terminal link error from vkcalls does not fall back to tokenChain", func(t *testing.T) {
		var (
			vkCallsCalled  atomic.Bool
			tokenChainHits atomic.Int32
		)

		c := newTestClient(t, func(_ context.Context, _ string, _ int, creds VKCredentials, _ tlsclient.CookieJar) (string, string, []string, error) {
			tokenChainHits.Add(1)
			return "std-u", "std-p", []string{"std:1"}, nil
		})
		c.vkCallsChain = func(ctx context.Context, link string, streamID int, jar tlsclient.CookieJar) (string, string, []string, error) {
			vkCallsCalled.Store(true)
			return "", "", nil, ErrInvalidJoinLink
		}

		_, _, _, err := c.GetCredentials(context.Background(), "call-id", 0)
		if err == nil || !errors.Is(err, ErrInvalidJoinLink) {
			t.Fatalf("expected ErrInvalidJoinLink, got %v", err)
		}
		if !vkCallsCalled.Load() {
			t.Fatalf("expected vkCallsChain to be called")
		}
		if tokenChainHits.Load() != 0 {
			t.Fatalf("expected tokenChain NOT to be called on fatal/terminal link error")
		}
	})
}
