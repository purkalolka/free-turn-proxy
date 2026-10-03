package vkauth

import (
	"context"
	"fmt"
	neturl "net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/namegen"

	tlsclient "github.com/bogdanfinn/tls-client"
)

const (
	vkCallsClientID   = "8093730"
	vkCallsAPIVersion = "5.276"
	vkCallsHost       = "api.vk.me"
)

// fetchVKCallsAnonToken выполняет шаг 1 VK Calls API: auth.getAnonymToken
func (c *Client) fetchVKCallsAnonToken(
	ctx context.Context,
	httpClient tlsclient.HttpClient,
	profile browserprofile.Profile,
	joinLink, deviceID, escapedName string,
) (string, error) {
	urlAddr := fmt.Sprintf(
		"https://%s/method/auth.getAnonymToken?v=%s&client_id=%s&link=%s&device_id=%s&anonymName=%s&lang=en",
		vkCallsHost,
		vkCallsAPIVersion,
		vkCallsClientID,
		neturl.QueryEscape(joinLink),
		deviceID,
		escapedName,
	)

	reqFn := c.requestFn
	if reqFn == nil {
		reqFn = c.doRequest
	}
	resp, err := reqFn(ctx, httpClient, profile, "", urlAddr)
	if err != nil {
		return "", err
	}
	if errObj, hasErr := resp["error"].(map[string]any); hasErr {
		if termErr := classifyLinkError(errObj); termErr != nil {
			return "", termErr
		}
		return "", fmt.Errorf("auth.getAnonymToken error: %v", errObj)
	}

	respMap, ok := resp["response"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected auth.getAnonymToken response: %v", resp)
	}
	token, ok := respMap["anonymous_token"].(string)
	if !ok || token == "" {
		token, ok = respMap["token"].(string)
	}
	if !ok || token == "" {
		return "", fmt.Errorf("missing anonymous_token/token in auth.getAnonymToken response: %v", resp)
	}
	return token, nil
}

// fetchVKCallsCallPreview выполняет шаг 2 VK Calls API: messages.getCallPreview
// Возвращает user_id и secret.
func (c *Client) fetchVKCallsCallPreview(
	ctx context.Context,
	httpClient tlsclient.HttpClient,
	profile browserprofile.Profile,
	token1, deviceID, joinLink string,
) (string, string, error) {
	urlAddr := fmt.Sprintf(
		"https://%s/method/messages.getCallPreview?v=%s&client_id=%s&anonymous_token=%s&device_id=%s&link=%s&extended=1&fields=first_name,last_name,photo_200&lang=en",
		vkCallsHost,
		vkCallsAPIVersion,
		vkCallsClientID,
		token1,
		deviceID,
		neturl.QueryEscape(joinLink),
	)

	reqFn := c.requestFn
	if reqFn == nil {
		reqFn = c.doRequest
	}
	resp, err := reqFn(ctx, httpClient, profile, "", urlAddr)
	if err != nil {
		return "", "", err
	}
	if errObj, hasErr := resp["error"].(map[string]any); hasErr {
		if termErr := classifyLinkError(errObj); termErr != nil {
			return "", "", termErr
		}
		return "", "", fmt.Errorf("messages.getCallPreview error: %v", errObj)
	}

	respMap, ok := resp["response"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("unexpected messages.getCallPreview response: %v", resp)
	}

	var userIDStr string
	switch v := respMap["user_id"].(type) {
	case float64:
		userIDStr = fmt.Sprintf("%.0f", v)
	case int64:
		userIDStr = fmt.Sprintf("%d", v)
	case int:
		userIDStr = fmt.Sprintf("%d", v)
	case string:
		userIDStr = v
	default:
		return "", "", fmt.Errorf("missing or invalid user_id in messages.getCallPreview response: %v", resp)
	}

	secret, ok := respMap["secret"].(string)
	if !ok || secret == "" {
		return "", "", fmt.Errorf("missing secret in messages.getCallPreview response: %v", resp)
	}

	return userIDStr, secret, nil
}

// fetchVKCallsAnonymCallToken выполняет шаг 3 VK Calls API: messages.getAnonymCallToken
// Возвращает token2.
func (c *Client) fetchVKCallsAnonymCallToken(
	ctx context.Context,
	httpClient tlsclient.HttpClient,
	profile browserprofile.Profile,
	token1, deviceID, joinLink, escapedName, userID, secret string,
) (string, error) {
	urlAddr := fmt.Sprintf(
		"https://%s/method/messages.getAnonymCallToken?v=%s&client_id=%s&anonymous_token=%s&device_id=%s&link=%s&name=%s&user_id=%s&secret=%s&lang=en",
		vkCallsHost,
		vkCallsAPIVersion,
		vkCallsClientID,
		token1,
		deviceID,
		neturl.QueryEscape(joinLink),
		escapedName,
		userID,
		secret,
	)

	reqFn := c.requestFn
	if reqFn == nil {
		reqFn = c.doRequest
	}
	resp, err := reqFn(ctx, httpClient, profile, "", urlAddr)
	if err != nil {
		return "", err
	}
	if errObj, hasErr := resp["error"].(map[string]any); hasErr {
		if termErr := classifyLinkError(errObj); termErr != nil {
			return "", termErr
		}
		return "", fmt.Errorf("messages.getAnonymCallToken error: %v", errObj)
	}

	respMap, ok := resp["response"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected messages.getAnonymCallToken response: %v", resp)
	}

	token2, ok := respMap["token"].(string)
	if !ok || token2 == "" {
		// Некоторые версии VK API могут вернуть anonymous_token или call_token
		if t, ok2 := respMap["anonymous_token"].(string); ok2 && t != "" {
			token2 = t
		} else {
			return "", fmt.Errorf("missing token in messages.getAnonymCallToken response: %v", resp)
		}
	}

	return token2, nil
}

func (c *Client) getVKCallsChain(ctx context.Context, link string, streamID int, jar tlsclient.CookieJar) (string, string, []string, error) {
	profile := c.currentPersona()

	httpClient, err := c.newTLSClient(profile, jar)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to initialize tls_client: %w", err)
	}

	name := namegen.Generate()
	escapedName := neturl.QueryEscape(name)
	deviceID := uuid.New().String()

	cleanLink := strings.TrimPrefix(link, "https://vk.com/call/join/")
	cleanLink = strings.TrimPrefix(cleanLink, "https://vk.ru/call/join/")
	joinLink := "https://vk.com/call/join/" + cleanLink

	c.log.Debugf("[STREAM %d] [VK Auth] Starting VK Calls API chain (client_id=%s) - Name: %s, DeviceID: %s",
		streamID, vkCallsClientID, name, deviceID)

	// Шаг 1: auth.getAnonymToken
	token1, err := c.fetchVKCallsAnonToken(ctx, httpClient, profile, joinLink, deviceID, escapedName)
	if err != nil {
		return "", "", nil, fmt.Errorf("VK Calls step 1 (getAnonymToken) failed: %w", err)
	}

	if delayErr := vkDelayRandom(ctx, 100, 200); delayErr != nil {
		return "", "", nil, delayErr
	}

	// Шаг 2: messages.getCallPreview
	userID, secret, err := c.fetchVKCallsCallPreview(ctx, httpClient, profile, token1, deviceID, joinLink)
	if err != nil {
		return "", "", nil, fmt.Errorf("VK Calls step 2 (getCallPreview) failed: %w", err)
	}

	if delayErr := vkDelayRandom(ctx, 100, 200); delayErr != nil {
		return "", "", nil, delayErr
	}

	// Шаг 3: messages.getAnonymCallToken
	token2, err := c.fetchVKCallsAnonymCallToken(ctx, httpClient, profile, token1, deviceID, joinLink, escapedName, userID, secret)
	if err != nil {
		return "", "", nil, fmt.Errorf("VK Calls step 3 (getAnonymCallToken) failed: %w", err)
	}

	if delayErr := vkDelayRandom(ctx, 100, 150); delayErr != nil {
		return "", "", nil, delayErr
	}

	// Шаг 4: sessionKey из c.fetchOkRuSession
	sessionKey, err := c.fetchOkRuSession(ctx, httpClient, profile)
	if err != nil {
		return "", "", nil, fmt.Errorf("VK Calls step 4 (fetchOkRuSession) failed: %w", err)
	}

	if delayErr := vkDelayRandom(ctx, 100, 150); delayErr != nil {
		return "", "", nil, delayErr
	}

	// Шаг 5: c.fetchTurnCreds
	return c.fetchTurnCreds(ctx, httpClient, profile, streamID, cleanLink, token2, sessionKey)
}
