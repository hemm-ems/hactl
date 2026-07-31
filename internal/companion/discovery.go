package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// DiscoveryReason categorises why companion discovery failed.
type DiscoveryReason string

const (
	// ReasonAuthDenied — the token is valid but lacks hassio_admin scope, so
	// Supervisor refuses to enumerate add-ons for it.
	ReasonAuthDenied DiscoveryReason = "auth_denied"
	// ReasonAuthInvalid — Home Assistant rejected the token outright. Not a
	// transport failure: the socket opened and HA said no.
	ReasonAuthInvalid DiscoveryReason = "auth_invalid"
	// ReasonAddonMissing — hactl-companion add-on is not installed.
	ReasonAddonMissing DiscoveryReason = "addon_missing"
	// ReasonUnreachable — nothing answered at the configured URL.
	ReasonUnreachable DiscoveryReason = "unreachable"
	// ReasonRedirected — the configured URL redirects to another origin, so the
	// REST half silently lands somewhere else and the WebSocket half cannot
	// follow at all.
	ReasonRedirected DiscoveryReason = "redirected"
	// ReasonProtocolMismatch — HA does not expose the Supervisor WS proxy.
	// Most often this means HA Container (no Supervisor) rather than HA OS/Supervised.
	ReasonProtocolMismatch DiscoveryReason = "protocol_mismatch"
)

// DiscoveryReasons is every reason discovery can fail with.
//
// It exists so the rule "each reason names its own fix" can be quantified over
// the set instead of over the reasons somebody remembered (H-24). The list is
// held to the const block above by TestDiscoveryReasonsMatchTheConstBlock,
// which reads the source: a reason added without a hint falls through to
// "check your network", which is the whole of #75.
func DiscoveryReasons() []DiscoveryReason {
	return []DiscoveryReason{
		ReasonAuthDenied,
		ReasonAuthInvalid,
		ReasonAddonMissing,
		ReasonUnreachable,
		ReasonRedirected,
		ReasonProtocolMismatch,
	}
}

// DiscoveryError is returned when the companion cannot be found, with a typed
// Reason so callers can render a targeted fix hint.
type DiscoveryError struct {
	Reason DiscoveryReason
	// cause is the transport or protocol error the reason was read off, kept so
	// a hint can quote what actually happened instead of describing a category.
	cause error
	msg   string
}

func (e *DiscoveryError) Error() string { return e.msg }

// Unwrap exposes the transport failure underneath, so a caller that cares about
// the redirect or the rejected token can match on the typed error rather than
// on this package's summary of it.
func (e *DiscoveryError) Unwrap() error { return e.cause }

// newDiscoveryError builds the failure and the remediation together.
//
// The two used to be able to disagree, and did: an authentication failure was
// reported as `discovery_reason: "unreachable"` with a hint that said "Check
// Ingress / network", one line under a `ws_error` reading `authentication
// failed: Invalid access token or password`. Three different root causes — a
// rejected token, a refused connection and a network blackhole — collapsed into
// one label and one wrong remediation (#75). Each reason now names its own fix,
// and the cause is quoted rather than paraphrased.
func newDiscoveryError(reason DiscoveryReason, cause error) *DiscoveryError {
	var hint string
	switch reason {
	case ReasonAuthDenied:
		hint = "companion not found (token lacks hassio_admin scope)\n\n" +
			"Auto-discovery uses the Supervisor WS proxy, which requires a long-lived\n" +
			"token created by an HA admin (owner). The current token is denied.\n\n" +
			"Fix: create a new long-lived token as an HA owner, or set COMPANION_URL in .env\n" +
			"     (Settings → Add-ons → hactl companion → Web UI → copy the URL)."
	case ReasonAuthInvalid:
		hint = "companion not found (Home Assistant rejected the token)\n\n" +
			"The connection succeeded and HA refused the credentials, so this is not a\n" +
			"network problem and COMPANION_URL will not help.\n\n" +
			"Fix: create a new long-lived access token (HA → your profile → Security) and\n" +
			"     put it in HA_TOKEN in .env."
	case ReasonAddonMissing:
		hint = "companion not found (add-on not installed)\n\n" +
			"Supervisor knows the add-ons on this instance and hactl-companion is not\n" +
			"among them.\n\n" +
			"Fix: install hactl-companion from HA → Settings → Add-ons, or set COMPANION_URL\n" +
			"     in .env if the add-on is reachable at a direct URL."
	case ReasonRedirected:
		hint = "companion not found (HA_URL redirects elsewhere)\n\n" +
			"The configured URL is not the origin that answers, and a WebSocket cannot\n" +
			"follow a redirect — so add-on discovery, which runs over the Supervisor WS\n" +
			"proxy, cannot start at all.\n\n" +
			"Fix: set HA_URL in .env to the origin the server redirects to."
	case ReasonProtocolMismatch:
		hint = "companion not found (HA does not expose the Supervisor WS proxy)\n\n" +
			"This usually means HA Container (Docker) rather than HA OS / Supervised — there\n" +
			"is no Supervisor to enumerate add-ons via WS.\n\n" +
			"Fix: set COMPANION_URL in .env to point directly at the companion."
	default:
		hint = "companion not found (unreachable)\n\n" +
			"Nothing answered at the configured HA_URL, so add-on discovery could not run.\n\n" +
			"Fix: check HA_URL and the network, or set COMPANION_URL in .env for a direct\n" +
			"     connection."
	}
	if cause != nil {
		hint += "\n\nThe connection failed with: " + cause.Error()
	}
	return &DiscoveryError{Reason: reason, cause: cause, msg: hint}
}

// classifyConnectError reads the reason off the error that stopped the
// WebSocket from opening.
//
// The typed cases come first and are the point: a redirect and a rejected token
// are facts the transport established, not guesses from a message. What is left
// is a genuine transport failure — a refused port, a DNS failure, a blackholed
// host — and those really are all "unreachable".
func classifyConnectError(err error) DiscoveryReason {
	if err == nil {
		return ReasonUnreachable
	}
	var re *haapi.RedirectError
	if errors.As(err, &re) {
		return ReasonRedirected
	}
	var ae *haapi.AuthError
	if errors.As(err, &ae) {
		return ReasonAuthInvalid
	}
	return ReasonUnreachable
}

// classifyWSError inspects the error message from a failed Supervisor WS call —
// a command that ran on an open, authenticated socket — and returns the most
// likely DiscoveryReason.
func classifyWSError(errMsg string) DiscoveryReason {
	lower := strings.ToLower(errMsg)
	if strings.Contains(lower, "unknown_command") ||
		strings.Contains(lower, "unknown_message_type") ||
		strings.Contains(lower, "unknown command") {
		return ReasonProtocolMismatch
	}
	if strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "401") ||
		strings.Contains(lower, "403") {
		return ReasonAuthDenied
	}
	if strings.Contains(lower, "not found") ||
		strings.Contains(lower, "addon not found") {
		return ReasonAddonMissing
	}
	return ReasonUnreachable
}

// addonEntry is the subset of /addons enumeration we use to pick the companion.
type addonEntry struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Version string `json:"version"`
	Ingress bool   `json:"ingress"`
}

// addonInfo is the subset of /addons/<slug>/info we use to build the URL.
type addonInfo struct {
	Slug       string `json:"slug"`
	State      string `json:"state"`
	Version    string `json:"version"`
	Ingress    bool   `json:"ingress"`
	IngressURL string `json:"ingress_url"`
}

// matchCompanion picks the companion add-on out of a Supervisor /addons list.
// Match strategy (in order):
//  1. slug exactly "hactl_companion" (local repo / dev install)
//  2. slug ends in "_hactl_companion" (Supervisor repo install: `<repoId>_hactl_companion`)
//  3. name equals "hactl companion" (case-insensitive) — last-resort fallback
//
// Returns empty string if no entry matches.
func matchCompanion(addons []addonEntry) string {
	for _, a := range addons {
		if a.Slug == "hactl_companion" {
			return a.Slug
		}
	}
	for _, a := range addons {
		if strings.HasSuffix(a.Slug, "_hactl_companion") {
			return a.Slug
		}
	}
	for _, a := range addons {
		if strings.EqualFold(a.Name, "hactl companion") {
			return a.Slug
		}
	}
	return ""
}

// Discover finds the companion URL.
// Priority:
//  1. Explicit COMPANION_URL from config (.env)
//  2. Enumerate add-ons via the Supervisor WS proxy (`supervisor/api`), pick the
//     companion by slug/name, then fetch its ingress URL.
//
// Returns the companion base URL or a *DiscoveryError if not found.
//
// A ws whose Connect failed is legal and is the interesting case: it carries
// the reason it failed, and that reason is the answer. Callers used to hand in
// nil for exactly that situation, which is why every connectivity failure came
// out as "unreachable" — a classification made from an absence cannot say more
// than "something is missing" (#75).
func Discover(ctx context.Context, cfg *config.Config, ws *haapi.WSClient) (string, error) {
	if cfg.CompanionURL != "" {
		slog.Debug("companion URL from config", "url", cfg.CompanionURL)
		return cfg.CompanionURL, nil
	}

	if ws == nil {
		return "", newDiscoveryError(ReasonUnreachable, nil)
	}
	if !ws.Connected() {
		connErr := ws.ConnectError()
		reason := classifyConnectError(connErr)
		slog.Debug("companion discovery has no websocket", "error", connErr, "reason", reason)
		return "", newDiscoveryError(reason, connErr)
	}

	listRaw, err := ws.SupervisorAPI(ctx, "/addons", "get", nil)
	if err != nil {
		reason := classifyWSError(err.Error())
		slog.Debug("supervisor/api /addons failed", "error", err, "reason", reason)
		return "", newDiscoveryError(reason, err)
	}

	var listResp struct {
		Addons []addonEntry `json:"addons"`
	}
	if jsonErr := json.Unmarshal(listRaw, &listResp); jsonErr != nil {
		return "", fmt.Errorf("parsing /addons response: %w", jsonErr)
	}
	if degErr := degeneracy.Check("supervisor /addons", &listResp); degErr != nil {
		return "", degErr
	}

	slug := matchCompanion(listResp.Addons)
	if slug == "" {
		slog.Debug("companion add-on not in Supervisor /addons list", "count", len(listResp.Addons))
		return "", newDiscoveryError(ReasonAddonMissing, nil)
	}

	infoRaw, err := ws.SupervisorAPI(ctx, "/addons/"+slug+"/info", "get", nil)
	if err != nil {
		reason := classifyWSError(err.Error())
		slog.Debug("supervisor/api /addons/<slug>/info failed", "slug", slug, "error", err, "reason", reason)
		return "", newDiscoveryError(reason, err)
	}

	var info addonInfo
	if jsonErr := json.Unmarshal(infoRaw, &info); jsonErr != nil {
		return "", fmt.Errorf("parsing /addons/%s/info response: %w", slug, jsonErr)
	}
	if degErr := degeneracy.Check("supervisor /addons/<slug>/info", &info); degErr != nil {
		return "", degErr
	}
	if info.IngressURL == "" {
		slog.Debug("companion add-on info has no ingress_url", "slug", slug, "state", info.State)
		return "", newDiscoveryError(ReasonUnreachable, nil)
	}

	url := strings.TrimRight(cfg.URL, "/") + "/" + strings.Trim(info.IngressURL, "/") + "/"
	slog.Debug("companion URL from Supervisor WS proxy", "slug", slug, "url", url)
	return url, nil
}
