# Android registration for new Audible connections

New account connections and reauthentication use an Android registration through
the patched go-audible dependency. Existing credentials keep their registered
device type until the user signs in again. The setup wizard and Settings use
the same profile when creating the login URL and exchanging the callback.

This addresses a tested failure: two fresh iPhone registrations, one through
Audplexus and one through audible-cli, received `CustomerThrottled` license
denials for purchased titles. A fresh Libation 14.2.2 Android registration
downloaded and decrypted The Steps successfully. That comparison identifies a
working registration path; it does not prove the cause of every denial.

The dependency implements a ten-byte Android serial and matching OAuth,
registration, token-refresh and request headers. An iPhone serial is a different
format and is not shortened. No existing credentials are converted silently.

The fork dependency is pinned for testing. Replace it with an upstream release
when the SDK change is available there. This change contains neither the error
formatter from PR #60 nor the request/cooldown changes from PR #61.

## References

- [AudibleApi #84: the Android serial correction](https://github.com/rmcrackan/AudibleApi/pull/84)
- [AudibleApi #83: registration profiles](https://github.com/rmcrackan/AudibleApi/pull/83)
- [Libation #2029: registration changes](https://github.com/rmcrackan/Libation/pull/2029)
- [Reddit discussion and maintainer reconnect instructions](https://www.reddit.com/r/audible/comments/1wbbzdb/throttling/)

AudibleApi #84 reports that the serial correction was sufficient in its author's
experiment; other metadata updates were not necessary for that result. Because
Audplexus previously registered as iPhone, this change adds the working Android
path rather than claiming Audplexus had that same Android serial defect.
