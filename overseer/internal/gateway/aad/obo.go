package aad

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
)

// GraphScope asks for a Graph token covering everything already consented to
// the app; offline_access makes the endpoint also return a refresh token.
const GraphScope = "https://graph.microsoft.com/.default offline_access"

// TokenSet is one grant response: the Graph access token, the refresh token
// that outlives it, and the absolute expiry computed from expires_in.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// OBOClient speaks to the tenant's v2.0 token endpoint: the on-behalf-of
// exchange for incoming user assertions, and the refresh grant that keeps
// background work alive between user sessions.
type OBOClient struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	TokenURL     string       // defaults to the tenant's v2.0 token endpoint
	HTTPClient   *http.Client // defaults to http.DefaultClient
	Now          func() time.Time
}

func (c *OBOClient) Exchange(ctx context.Context, assertion string) (TokenSet, error) {
	return c.post(ctx, url.Values{
		"grant_type":          {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"requested_token_use": {"on_behalf_of"},
		"client_id":           {c.ClientID},
		"client_secret":       {c.ClientSecret},
		"assertion":           {assertion},
		"scope":               {GraphScope},
	})
}

func (c *OBOClient) Refresh(ctx context.Context, refreshToken string) (TokenSet, error) {
	return c.post(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"refresh_token": {refreshToken},
		"scope":         {GraphScope},
	})
}

func (c *OBOClient) post(ctx context.Context, form url.Values) (TokenSet, error) {
	endpoint := c.TokenURL
	if endpoint == "" {
		endpoint = "https://login.microsoftonline.com/" + c.TenantID + "/oauth2/v2.0/token"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return TokenSet{}, fmt.Errorf("token endpoint: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return TokenSet{}, fmt.Errorf("read token response: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		var aadErr AADError
		if json.Unmarshal(body, &aadErr) != nil || aadErr.Code == "" {
			return TokenSet{}, fmt.Errorf("token endpoint returned %d", res.StatusCode)
		}
		return TokenSet{}, &aadErr
	}
	var grant struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &grant); err != nil || grant.AccessToken == "" {
		return TokenSet{}, fmt.Errorf("token endpoint returned unusable success body")
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	return TokenSet{
		AccessToken:  grant.AccessToken,
		RefreshToken: grant.RefreshToken,
		ExpiresAt:    now().Add(time.Duration(grant.ExpiresIn) * time.Second),
	}, nil
}

// AADError is a non-200 from the token endpoint. Description can carry
// AADSTS codes; token values never appear in it.
type AADError struct {
	Code        string `json:"error"`
	Suberror    string `json:"suberror"`
	Description string `json:"error_description"`
}

func (e *AADError) Error() string {
	return fmt.Sprintf("aad token endpoint: %s (suberror %q)", e.Code, e.Suberror)
}

// Is reports entity.ErrReauthRequired for the "user must come back" family so
// callers distinguish reconnect-your-mailbox from plain outages.
func (e *AADError) Is(target error) bool {
	return target == entity.ErrReauthRequired && e.needsInteraction()
}

// needsInteraction: AAD signals it three ways — error=interaction_required,
// invalid_grant suberrors, or AADSTS codes in the description (65001 consent,
// 50076/50079 MFA, 53003 conditional access, 50173/70008 expired or revoked
// grants).
func (e *AADError) needsInteraction() bool {
	if e.Code == "interaction_required" {
		return true
	}
	switch e.Suberror {
	case "consent_required", "basic_action", "message_only", "conditional_access":
		return true
	}
	for _, code := range []string{"AADSTS65001", "AADSTS50076", "AADSTS50079", "AADSTS53003", "AADSTS50173", "AADSTS70008"} {
		if strings.Contains(e.Description, code) {
			return true
		}
	}
	return false
}
