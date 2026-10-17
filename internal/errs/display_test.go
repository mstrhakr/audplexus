package errs

import (
	"strings"
	"testing"
)

func TestCleanEntitlementReason(t *testing.T) {
	const throttled = "Audible is throttling download licenses (CustomerThrottled). Wait before retrying; purchased books can also be affected."
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "throttling wins over membership ownership client and Plus checks",
			in: "license denied for ASIN B000EXAMPLE (status=Denied): License not granted to customer example-customer; reasons: " +
				"Customer is not part of any plans [RequesterEligibility/Membership] | " +
				"No ownership information returned [CustomerThrottled/Ownership] | " +
				"Client does not have access [RequesterEligibility/Client] | " +
				"Asin is not eligible for AYCL [ContentEligibility/AYCL]",
			want: throttled,
		},
		{
			name: "throttling code is case insensitive",
			in:   "license denied; reasons: [CUSTOMERTHROTTLED/Ownership]",
			want: throttled,
		},
		{
			name: "throttling described in message",
			in:   "license denied; reasons: Customer example-customer being throttled [ContentEligibility/AYCL]",
			want: throttled,
		},
		{name: "membership only", in: "Customer is not part of any plans [RequesterEligibility/Membership]", want: "No longer included with your Audible plan"},
		{name: "ownership only", in: "No ownership [RequesterEligibility/Ownership]", want: "Audible reports no ownership of this title"},
		{name: "Plus only", in: "Asin is not eligible for AYCL [ContentEligibility/AYCL]", want: "No longer included with Audible Plus"},
		{name: "generic denial", in: "license denied", want: "Audible denied the download license"},
		{name: "empty", in: " \n ", want: "Audible entitlement denied"},
		{name: "unknown reason", in: "  unexpected response  ", want: "unexpected response"},
		{name: "long unknown reason", in: strings.Repeat("x", 200), want: strings.Repeat("x", 160) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanEntitlementReason(tc.in); got != tc.want {
				t.Fatalf("CleanEntitlementReason(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCleanForDisplay(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plex 404 with html body",
			in:   `plex section items returned 404: <html><head><title>Not Found</title></head><body><h1>404 Not Found</h1></body></html>`,
			want: "Server reachable but the configured library/section was not found.",
		},
		{
			name: "401 auth failure",
			in:   "emby items returned 401: Access token is required.",
			want: "Authentication failed — check the token or API key.",
		},
		{
			name: "503 upstream",
			in:   "abs /api/libraries returned 503: service unavailable",
			want: "Upstream server error — the media server returned 503.",
		},
		{
			name: "non-http error keeps text",
			in:   "dial tcp 10.0.0.5:32400: connect: connection refused",
			want: "dial tcp 10.0.0.5:32400: connect: connection refused",
		},
		{
			name: "html only, no http code",
			in:   `<html><body><p>oops</p></body></html>`,
			want: "oops",
		},
		{
			name: "embedded h1 collapses",
			in:   `plex scan returned 404: <html><body><h1>404 Not Found</h1></body></html>`,
			want: "Server reachable but the configured library/section was not found.",
		},
		{
			name: "empty",
			in:   "  ",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanForDisplay(tc.in)
			if got != tc.want {
				t.Fatalf("CleanForDisplay(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}
