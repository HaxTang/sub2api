package service

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"golang.org/x/net/http/httpguts"
)

const defaultAccountTestUserAgent = "codex-tui/0.160.0 (Windows 10.0.19045; x86_64) WindowsTerminal (codex-tui; 0.160.0)"

// Keep this optional capability separate from UsageLogRepository so existing
// repository adapters do not need to implement an admin-only query.
type accountTestUserAgentReader interface {
	GetLatestUserAgent(ctx context.Context) (string, error)
}

type accountTestUserAgentContextKey struct{}

// Resolve once per admin test, not once per protocol, poll or retry. The value
// stays request-scoped: it never changes account credentials or real forwarding.
func (s *AccountTestService) withAccountTestUserAgent(ctx context.Context, account *Account, modelID string) context.Context {
	if account == nil || account.IsAnthropic() {
		return ctx
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		switch {
		case account.Platform == PlatformAntigravity:
			modelID = defaultAntigravityTestModel
		case account.RoutesProtocolByInbound() || account.routesByModel():
			modelID = account.providerDefaultTestModel()
		}
	}
	if strings.Contains(strings.ToLower(modelID), "claude") ||
		strings.Contains(strings.ToLower(account.GetMappedModel(modelID)), "claude") {
		return ctx
	}

	userAgent := defaultAccountTestUserAgent
	if s.userAgentReader != nil {
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		latest, err := s.userAgentReader.GetLatestUserAgent(queryCtx)
		cancel()
		if err != nil {
			log.Printf("[AccountTest] latest usage User-Agent lookup failed; using fallback: %v", err)
		} else if len(latest) <= 512 && httpguts.ValidHeaderFieldValue(latest) && !strings.ContainsAny(latest, "\r\n") {
			if latest = strings.TrimSpace(latest); latest != "" {
				userAgent = latest
			}
		}
	}
	return context.WithValue(ctx, accountTestUserAgentContextKey{}, userAgent)
}

// ApplyAccountTestUserAgent lets the shared transport retain the test-only UA
// after host-specific defaults or compatibility fallbacks. Real user requests
// never carry this private context value and are left unchanged.
func ApplyAccountTestUserAgent(req *http.Request) {
	if req != nil {
		applyAccountTestUserAgent(req.Context(), req.Header)
	}
}

// Apply after platform defaults and account overrides, immediately before the
// test request is sent. Contexts without a test UA (including Claude) are no-ops.
func applyAccountTestUserAgent(ctx context.Context, headers http.Header) {
	userAgent, _ := ctx.Value(accountTestUserAgentContextKey{}).(string)
	if userAgent == "" || headers == nil {
		return
	}
	hasCodexIdentity := false
	for key := range headers {
		switch {
		case strings.EqualFold(key, "User-Agent"):
			delete(headers, key)
		case strings.EqualFold(key, "originator"), strings.EqualFold(key, "version"):
			hasCodexIdentity = true
			delete(headers, key)
		}
	}
	headers.Set("User-Agent", userAgent)

	// Do not normalize or upgrade the selected UA. Pair recognized Codex clients
	// with their own identity/version; other clients must not inherit a false
	// Codex identity left behind by synthetic probe defaults.
	if hasCodexIdentity {
		originator, pairedUA, ok := openai.PairCodexClientIdentity(userAgent)
		if ok && pairedUA == userAgent {
			headers.Set("originator", originator)
			if version := NormalizeCodexClientVersion(openai.CodexUserAgentVersion(userAgent)); version != "" {
				headers.Set("version", version)
			}
		}
	}
}

// Antigravity connection tests share a retry loop with real forwarding. Wrap
// only that test's transport instead of changing the shared service or loop.
type accountTestUserAgentUpstream struct {
	HTTPUpstream
}

func (u accountTestUserAgentUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	applyAccountTestUserAgent(req.Context(), req.Header)
	return u.HTTPUpstream.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u accountTestUserAgentUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	applyAccountTestUserAgent(req.Context(), req.Header)
	return u.HTTPUpstream.DoWithTLS(req, proxyURL, accountID, accountConcurrency, profile)
}
