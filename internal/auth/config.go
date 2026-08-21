package auth

// Settings keys for the auth subsystem. All persisted in the existing
// settings key/value table.
const (
	SettingKeyAuthMethod   = "auth_method"
	SettingKeyAuthRequired = "auth_required"
)

// AuthMethod is the top-level auth mode.
type AuthMethod string

const (
	AuthMethodNone  AuthMethod = "none"
	AuthMethodForms AuthMethod = "forms"
)

// Valid reports whether the value is a known method.
func (m AuthMethod) Valid() bool {
	return m == AuthMethodNone || m == AuthMethodForms
}

// AuthRequired is the per-origin gate that determines which clients get to
// skip authentication. Mirrors Sonarr's AuthenticationRequiredType.
type AuthRequired string

const (
	AuthRequiredEnabled              AuthRequired = "enabled"
	AuthRequiredDisabledForLocal     AuthRequired = "disabled_for_local"
	AuthRequiredDisabledForLocalhost AuthRequired = "disabled_for_localhost"
)

// Valid reports whether the value is a known mode.
func (r AuthRequired) Valid() bool {
	switch r {
	case AuthRequiredEnabled, AuthRequiredDisabledForLocal, AuthRequiredDisabledForLocalhost:
		return true
	}
	return false
}
