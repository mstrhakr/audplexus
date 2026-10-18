# Reconnect with an alternative Audible device registration

Audplexus keeps iPhone as the default for setup, new accounts, and reconnects.
Android is an explicit alternative for an existing account whose purchased books
fail to download.

In Settings → Audible Account, expand **Reconnect this account**, choose
**Android (alternative)**, and generate an Amazon login link. Complete the new
sign-in and paste its redirect URL back into Audplexus. Multiple-account installs
offer this control on each account’s row.

Starting reconnect leaves the saved credentials in place. Completing sign-in
replaces the connection on that same account; it does not create a second
account, reset the library, or remove downloaded files. The selection applies to
that sign-in. It does not describe or convert the account’s current registration.
The login URL and callback use the same profile, and saved credentials retain
that profile after a restart.

## Why offer Android?

In the reported case, a library of over 1,000 titles repeatedly received
`CustomerThrottled` with iPhone registrations for roughly eight hours. Fresh
sign-ins through Audplexus and audible-cli did not resolve the refusal. The
books still played in the official Audible app. A fresh registration using
Libation’s corrected Android default worked. Audplexus then downloaded and
decrypted two affected purchases with that profile, including one after a
container restart without another sign-in.

The [Libation discussion](https://github.com/rmcrackan/Libation/issues/2021#issuecomment-5548682504)
also contains reports of the reverse: iPhone worked where Android failed.
Its maintainer [warned that the underlying problem was more complex than the platform labels](https://github.com/rmcrackan/Libation/issues/2021#issuecomment-5549285399).
An AudibleApi contributor [recommended retaining both registrations as a backup](https://github.com/rmcrackan/Libation/issues/2021#issuecomment-5564352730).
These observations support offering a choice. They do not establish separate
platform rate limits or guarantee that Android resolves every refusal.

## Implementation and references

The patched go-audible dependency implements a ten-byte Android serial and
matching OAuth, registration, token-refresh and request headers. It also accepts
the base64 DER signing key returned by Android registration. iPhone keeps its
existing serial format and remains the SDK default.

- [AudibleApi #84: Android serial correction and the need for fresh registration](https://github.com/rmcrackan/AudibleApi/pull/84)
- [Libation #2029: registration profile selection](https://github.com/rmcrackan/Libation/pull/2029)
- [Reddit maintainer instructions for recovery through a fresh sign-in](https://www.reddit.com/r/audible/comments/1wbbzdb/comment/p8tem3k/)
- [go-audible #1: SDK dependency](https://github.com/mstrhakr/go-audible/pull/1)

AudibleApi #84 identifies an Android serial-length defect in its implementation.
Audplexus previously registered as iPhone, so this adds a working alternative
rather than attributing that Android defect to Audplexus.

The SDK fork is pinned pending an upstream release. This change excludes the
error formatting from #60 and the request/cooldown changes from #61.
