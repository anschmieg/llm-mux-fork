package kiro

import (
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultRegion is the default AWS region for Kiro.
	DefaultRegion = "us-east-1"

	// KiroRefreshURL is the Kiro-specific refresh endpoint for social auth (Google, etc.)
	KiroRefreshURL = "https://prod.%s.auth.desktop.kiro.dev/refreshToken"

	// OIDCRefreshURL is the AWS OIDC refresh endpoint for IAM/SSO auth
	OIDCRefreshURL = "https://oidc.%s.amazonaws.com/token"
)

// KiroRefreshResponse represents the response from Kiro refresh endpoint.
type KiroRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	ProfileArn   string `json:"profileArn"`
}

// RefreshTokens attempts to refresh the access token using the refresh token.
// For social auth (Google, etc.), uses Kiro's dedicated refresh endpoint.
// For IAM/SSO auth, uses AWS OIDC endpoint with client credentials.
func RefreshTokens(creds *KiroCredentials) (*KiroCredentials, error) {
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	region := creds.Region
	if region == "" {
		region = DefaultRegion
	}
	if creds.IDCRegion != "" {
		region = creds.IDCRegion
	}

	// Determine auth method - default to social for tokens from Kiro IDE
	authMethod := creds.AuthMethod
	if authMethod == "" {
		authMethod = "social"
	}

	var tokenURL string
	var reqBody []byte

	if authMethod == "social" {
		// Use Kiro's dedicated refresh endpoint for social auth
		tokenURL = fmt.Sprintf(KiroRefreshURL, region)
		payload := map[string]string{
			"refreshToken": creds.RefreshToken,
		}
		reqBody, _ = json.Marshal(payload)
	} else {
		// Use AWS OIDC endpoint for IAM/SSO auth. Current Kiro CLI builder-id
		// credentials expect a JSON payload, not form-encoded data.
		tokenURL = fmt.Sprintf(OIDCRefreshURL, region)
		payload := map[string]string{
			"refreshToken": creds.RefreshToken,
			"grantType":    "refresh_token",
		}
		if creds.ClientID != "" {
			payload["clientId"] = creds.ClientID
		}
		if creds.ClientSecret != "" {
			payload["clientSecret"] = creds.ClientSecret
		}
		reqBody, _ = json.Marshal(payload)
	}

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if authMethod == "social" {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Create updated credentials
	newCreds := *creds
	newCreds.Type = "kiro" // Ensure type is set for watcher detection

	if authMethod == "social" {
		var kiroResp KiroRefreshResponse
		if err := json.NewDecoder(resp.Body).Decode(&kiroResp); err != nil {
			return nil, fmt.Errorf("failed to decode refresh response: %w", err)
		}
		newCreds.AccessToken = kiroResp.AccessToken
		if kiroResp.RefreshToken != "" {
			newCreds.RefreshToken = kiroResp.RefreshToken
		}
		newCreds.ExpiresIn = kiroResp.ExpiresIn
		newCreds.ExpiresAt = time.Now().Add(time.Duration(kiroResp.ExpiresIn) * time.Second)
		if kiroResp.ProfileArn != "" {
			newCreds.ProfileArn = kiroResp.ProfileArn
		}
	} else {
		var tokenResp map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			return nil, fmt.Errorf("failed to decode token response: %w", err)
		}

		accessToken, _ := tokenResp["accessToken"].(string)
		if accessToken == "" {
			accessToken, _ = tokenResp["access_token"].(string)
		}
		refreshToken, _ := tokenResp["refreshToken"].(string)
		if refreshToken == "" {
			refreshToken, _ = tokenResp["refresh_token"].(string)
		}
		tokenType, _ := tokenResp["tokenType"].(string)
		if tokenType == "" {
			tokenType, _ = tokenResp["token_type"].(string)
		}
		expiresIn := 0
		switch v := tokenResp["expiresIn"].(type) {
		case float64:
			expiresIn = int(v)
		case int:
			expiresIn = v
		}
		if expiresIn == 0 {
			switch v := tokenResp["expires_in"].(type) {
			case float64:
				expiresIn = int(v)
			case int:
				expiresIn = v
			}
		}
		if accessToken == "" {
			return nil, fmt.Errorf("failed to decode token response: missing access token")
		}
		newCreds.AccessToken = accessToken
		if refreshToken != "" {
			newCreds.RefreshToken = refreshToken
		}
		newCreds.ExpiresIn = expiresIn
		if expiresIn > 0 {
			newCreds.ExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
		}
		newCreds.TokenType = tokenType
	}

	return &newCreds, nil
}
