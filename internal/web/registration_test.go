package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/auth"
	"github.com/mstrhakr/audplexus/internal/crypto"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/library"
	audible "github.com/mstrhakr/go-audible"
)

func registrationContext(method string, form url.Values) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(method, "/auth/callback", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c
}

func TestRegistrationProfileSelection(t *testing.T) {
	for _, value := range []string{"", "iphone", "android", "other"} {
		t.Run(value, func(t *testing.T) {
			profile, err := parseRegistrationProfile(value)
			if value == "other" {
				if err == nil {
					t.Fatal("unknown profile accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (profile == audible.Android) != (value == "android") {
				t.Fatalf("wrong profile for %q", value)
			}
		})
	}
}

func TestRegistrationCallbackKeepsSelectedProfile(t *testing.T) {
	for _, profile := range []audible.DeviceProfile{audible.IPhone, audible.Android} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(registrationProfileName(profile)+"/"+method, func(t *testing.T) {
				s := &Server{}
				login, err := audible.NewClient(audible.MarketplaceUS).GetAuthURLForDevice(profile)
				if err != nil {
					t.Fatal(err)
				}
				s.rememberPendingAuth("existing-account", login.CodeVerifier, login.DeviceSerial, profile)
				form := url.Values{}
				if method == http.MethodPost {
					form.Set("device_profile", registrationProfileName(profile))
					form.Set("code_verifier", login.CodeVerifier)
					form.Set("device_serial", login.DeviceSerial)
				}
				request, err := s.registrationForCallback(registrationContext(method, form), "existing-account")
				if err != nil {
					t.Fatal(err)
				}
				if request.Profile != profile || request.CodeVerifier != login.CodeVerifier || request.DeviceSerial != login.DeviceSerial {
					t.Fatalf("callback lost its selected registration: %+v", request)
				}
				if len(s.pendingAuth.entries) != 0 {
					t.Fatal("completed sign-in state retained")
				}
				if _, err := s.registrationForCallback(registrationContext(http.MethodGet, nil), "existing-account"); err == nil {
					t.Fatal("expired GET callback accepted")
				}
			})
		}
	}
}

func TestRegistrationCallbackRejectsChangedSignIn(t *testing.T) {
	for _, field := range []string{"device_profile", "code_verifier", "device_serial"} {
		t.Run(field, func(t *testing.T) {
			s := &Server{}
			s.rememberPendingAuth("account", "verifier", "serial", audible.Android)
			form := url.Values{field: {"different"}}
			if field == "device_profile" {
				form.Set(field, "iphone")
			}
			if _, err := s.registrationForCallback(registrationContext(http.MethodPost, form), "account"); err == nil {
				t.Fatal("changed sign-in accepted")
			}
			if len(s.pendingAuth.entries) != 1 {
				t.Fatal("invalid callback discarded valid pending sign-in")
			}
		})
	}
}

func TestReconnectKeepsAccountAndCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, choice := range []string{"", "iphone", "android", "unknown"} {
		t.Run("profile="+choice, func(t *testing.T) {
			db := newWebTestDB(t)
			ctx := context.Background()
			account := &database.AudibleAccount{ID: "existing-account", DisplayName: "Test account", Marketplace: "us", Enabled: true, Credentials: []byte("synthetic stored credentials")}
			if err := db.CreateAudibleAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			before, err := db.GetAudibleAccount(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{db: db, router: gin.New(), audible: audible.NewClient(audible.MarketplaceUS)}
			s.setupTemplates()
			s.router.POST("/auth/start", s.handleAuthStart)
			form := url.Values{"account_id": {account.ID}, "marketplace": {"us"}, "device_profile": {choice}}
			req := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			s.router.ServeHTTP(response, req)
			if choice == "unknown" {
				if response.Code != http.StatusBadRequest {
					t.Fatalf("unknown profile status %d", response.Code)
				}
				after, _ := db.GetAudibleAccount(ctx, account.ID)
				if !reflect.DeepEqual(after, before) || len(s.pendingAuth.entries) != 0 {
					t.Fatal("invalid choice changed account or started sign-in")
				}
				return
			}
			if response.Code != http.StatusOK {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			want := "iphone"
			if choice == "android" {
				want = "android"
			}
			if !strings.Contains(response.Body.String(), `name="device_profile" value="`+want+`"`) {
				t.Fatal("callback form lost selected profile")
			}
			if !strings.Contains(response.Body.String(), `name="account_id" value="existing-account"`) {
				t.Fatal("callback targets a different account")
			}
			after, err := db.GetAudibleAccount(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatal("starting reconnect changed the existing account")
			}
			accounts, _ := db.ListAudibleAccounts(ctx)
			if len(accounts) != 1 {
				t.Fatal("reconnect created another account")
			}
		})
	}
}

func TestSettingsOffersReconnectForSingleAndMultipleAccounts(t *testing.T) {
	s := &Server{router: gin.New()}
	s.setupTemplates()
	for _, multiple := range []bool{false, true} {
		var output bytes.Buffer
		data := gin.H{"Authenticated": true, "MultiAccount": multiple, "Accounts": []accountView{{ID: "account", IsPrimary: true, Marketplace: "us"}}}
		err := s.router.HTMLRender.(*multiRender).templates["settings.html"].ExecuteTemplate(&output, "content", data)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"Reconnect this account", `value="iphone" selected`, `value="android"`, `name="account_id" value="account"`} {
			if !strings.Contains(output.String(), text) {
				t.Fatalf("multiple=%v: missing %s", multiple, text)
			}
		}
	}
}

// These tests run serially and restore the transport before parallel tests start.
// Amazon registration is simulated; no account or credentials leave the test.
type registrationTransport func(*http.Request) (*http.Response, error)

func (transport registrationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestReconnectRegistersAndPersistsBothProfiles(t *testing.T) {
	for _, profile := range []audible.DeviceProfile{audible.IPhone, audible.Android} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(registrationProfileName(profile)+"/"+method, func(t *testing.T) {
				ctx := context.Background()
				db := newWebTestDB(t)
				account := &database.AudibleAccount{ID: "existing-account", DisplayName: "Test account", Marketplace: "us", Enabled: true}
				if err := db.CreateAudibleAccount(ctx, account); err != nil {
					t.Fatal(err)
				}
				box, err := crypto.NewBox(bytes.Repeat([]byte{7}, 32))
				if err != nil {
					t.Fatal(err)
				}
				previousCredentials, err := json.Marshal(audible.Credentials{
					AccessToken: "previous-synthetic-access", RefreshToken: "previous-synthetic-refresh",
					ExpiresAt: time.Now().Add(time.Hour), Marketplace: "us",
					DeviceInfo: audible.DeviceInfo{DeviceType: "A10KISP2GWF0E4"},
				})
				if err != nil {
					t.Fatal(err)
				}
				account.Credentials, err = box.Encrypt(previousCredentials)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.UpdateAudibleAccount(ctx, account); err != nil {
					t.Fatal(err)
				}
				s := &Server{db: db, router: gin.New(), accounts: library.NewAccountManager(ctx, db, box)}
				s.refreshPrimaryAudible()
				s.setupTemplates()
				s.onboarded.Store(true)
				s.router.POST("/auth/start", s.handleAuthStart)
				s.router.Handle(method, "/auth/callback", s.handleAuthCallback)
				form := url.Values{"account_id": {account.ID}, "marketplace": {"us"}, "device_profile": {registrationProfileName(profile)}}
				start := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader(form.Encode()))
				start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				response := httptest.NewRecorder()
				s.router.ServeHTTP(response, start)
				if response.Code != http.StatusOK {
					t.Fatalf("start status %d", response.Code)
				}
				unchanged, _ := db.GetAudibleAccount(ctx, account.ID)
				if !bytes.Equal(unchanged.Credentials, account.Credentials) {
					t.Fatal("starting reconnect replaced working Android credentials")
				}
				pending := s.pendingAuth.entries[account.ID]
				wantHost := "api.audible.com"
				wantType := audible.DeviceTypeID
				if profile == audible.Android {
					wantType = "A10KISP2GWF0E4"
					wantHost = "api.amazon.com"
				}
				originalTransport := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = originalTransport })
				registered := false
				http.DefaultTransport = registrationTransport(func(request *http.Request) (*http.Response, error) {
					if request.URL.Host != wantHost || request.URL.Path != "/auth/register" {
						t.Fatalf("unexpected network request: %s", request.URL)
					}
					var body struct {
						Registration map[string]any `json:"registration_data"`
						Auth         map[string]any `json:"auth_data"`
					}
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.Registration["device_type"] != wantType || body.Registration["device_serial"] != pending.deviceSerial || body.Auth["code_verifier"] != pending.codeVerifier {
						t.Fatal("registration differs from selected sign-in")
					}
					registered = true
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"response":{"success":{"tokens":{"bearer":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","expires_in":3600},"mac_dms":{"adp_token":"synthetic-adp","device_private_key":"synthetic-key"}},"extensions":{"customer_info":{"user_id":"synthetic-customer"}}}}}`))}, nil
				})
				callbackURL := "/auth/callback?account_id=" + account.ID + "&openid.oa2.authorization_code=synthetic-code"
				callbackForm := url.Values{}
				if method == http.MethodPost {
					callbackURL = "/auth/callback"
					callbackForm = url.Values{"account_id": {account.ID}, "device_profile": {registrationProfileName(profile)}, "code_verifier": {pending.codeVerifier}, "device_serial": {pending.deviceSerial}, "redirect_url": {"https://www.audible.com/ap/maplanding?openid.oa2.authorization_code=synthetic-code"}}
				}
				callback := httptest.NewRequest(method, callbackURL, strings.NewReader(callbackForm.Encode()))
				callback.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				response = httptest.NewRecorder()
				s.router.ServeHTTP(response, callback)
				if !registered || response.Code != http.StatusOK {
					t.Fatalf("registration=%v, status=%d: %s", registered, response.Code, response.Body.String())
				}
				accounts, _ := db.ListAudibleAccounts(ctx)
				if len(accounts) != 1 || accounts[0].ID != account.ID || !crypto.IsEncrypted(accounts[0].Credentials) {
					t.Fatal("reconnect did not save encrypted credentials on the same account")
				}
				reloaded := library.NewAccountManager(ctx, db, box).ClientForAccount(account.ID)
				if reloaded == nil || reloaded.GetCredentials().DeviceInfo.DeviceType != wantType || reloaded.GetCredentials().AccessToken != "synthetic-access" {
					t.Fatal("saved connection lost selected profile after reload")
				}
			})
		}
	}
}

func TestNewAccountAndSetupUseIPhone(t *testing.T) {
	for _, flow := range []string{"add-account", "setup"} {
		t.Run(flow, func(t *testing.T) {
			ctx := context.Background()
			db := newWebTestDB(t)
			s := &Server{db: db, router: gin.New(), audible: audible.NewClient(audible.MarketplaceUS), accounts: library.NewAccountManager(ctx, db, nil), authMgr: auth.NewManager(db)}
			s.setupTemplates()
			response := httptest.NewRecorder()
			if flow == "add-account" {
				s.router.POST("/accounts/add", s.handleAccountAdd)
				request := httptest.NewRequest(http.MethodPost, "/accounts/add", strings.NewReader(url.Values{"marketplace": {"us"}, "display_name": {"Test account"}}.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				s.router.ServeHTTP(response, request)
			} else {
				s.router.GET("/setup", s.handleSetupWizard)
				s.router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setup?step=1", nil))
			}
			if response.Code != http.StatusOK {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `name="device_profile" value="iphone"`) {
				t.Fatal("initial sign-in did not use iPhone")
			}
			if len(s.pendingAuth.entries) != 1 {
				t.Fatal("initial sign-in missing pending state")
			}
			for _, pending := range s.pendingAuth.entries {
				if pending.profile != audible.IPhone || len(pending.deviceSerial) != 32 {
					t.Fatal("initial sign-in generated another profile")
				}
			}
		})
	}
}
