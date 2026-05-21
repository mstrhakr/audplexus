// Package errs holds helpers for turning raw backend error strings —
// often containing HTML response bodies from upstream media servers —
// into something safe and compact for the UI to show. Centralized here
// so every code path (web handlers, sync phase messages, dashboard
// recap, drift inbox) renders the same wording for the same failure.
package errs

import (
	"regexp"
	"strings"
)

// errTagRe matches an HTML tag for stripping. The bodies our backends
// embed in error strings are typically tiny upstream-server error pages
// (Plex / Emby / Jellyfin / ABS), so a regex is cheaper than spinning
// up html/parser and we don't need element-level fidelity here.
var errTagRe = regexp.MustCompile(`<[^>]*>`)

// errSpaceRe collapses runs of whitespace (including the newlines left
// behind after stripping <head>/<body>/<title> blocks) into a single space.
var errSpaceRe = regexp.MustCompile(`\s+`)

// errHTTPCodeRe extracts the numeric status code from a "<verb> returned
// <code>: <body>" error string. The first capture is the code itself.
var errHTTPCodeRe = regexp.MustCompile(`returned (\d{3})[:\s]`)

// CleanForDisplay turns a raw backend error message — which may embed
// an HTML error page from Plex/Emby/Jellyfin/ABS — into something that
// fits on one line in a UI badge. It:
//   - strips HTML tags
//   - collapses whitespace
//   - rewrites common HTTP status codes into friendly hints
//   - truncates very long messages so a 2KB upstream body doesn't
//     blow out the card layout
//
// The raw error is still logged at the original emission site, so we
// don't lose forensic detail.
func CleanForDisplay(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	var hint string
	if m := errHTTPCodeRe.FindStringSubmatch(s); m != nil {
		switch m[1] {
		case "401":
			hint = "Authentication failed — check the token or API key."
		case "403":
			hint = "Authentication accepted but the account is not authorized for this resource."
		case "404":
			hint = "Server reachable but the configured library/section was not found."
		case "500", "502", "503", "504":
			hint = "Upstream server error — the media server returned " + m[1] + "."
		}
	}

	s = errTagRe.ReplaceAllString(s, " ")
	s = errSpaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	const maxLen = 200
	if len(s) > maxLen {
		s = strings.TrimSpace(s[:maxLen]) + "…"
	}

	if hint != "" {
		return hint
	}
	return s
}

// CleanEntitlementReason turns Audible's verbose license-denied error
// (a multi-clause string listing every eligibility check that failed,
// often 600+ chars) into a short user-facing reason. Examples of input:
//
//	license denied for ASIN B0... (status=Denied): License not granted to
//	customer ... for asin [B0...]; reasons: Customer is not part of any
//	plans [RequesterEligibility/Membership] | Ownership: ... | ...
//
// The user just needs to know "Audible says you don't have access" plus
// a hint at WHY (no membership, ownership not found, AYCL ineligible).
// We keep the full raw message in logs, not in the UI.
func CleanEntitlementReason(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "Audible entitlement denied"
	}
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "not part of any plans"),
		strings.Contains(lower, "requestereligibility/membership"):
		return "No longer included with your Audible plan"
	case strings.Contains(lower, "no ownership"),
		strings.Contains(lower, "requestereligibility/ownership"):
		return "Audible reports no ownership of this title"
	case strings.Contains(lower, "not eligible for aycl"),
		strings.Contains(lower, "contenteligibility/aycl"):
		return "No longer included with Audible Plus"
	case strings.Contains(lower, "license denied"),
		strings.Contains(lower, "license not granted"):
		return "Audible denied the download license"
	}
	const maxLen = 160
	if len(s) > maxLen {
		s = strings.TrimSpace(s[:maxLen]) + "…"
	}
	return s
}
