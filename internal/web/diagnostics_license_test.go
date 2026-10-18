package web

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mstrhakr/audplexus/internal/database"
	"github.com/mstrhakr/audplexus/internal/library"
	audible "github.com/mstrhakr/go-audible"
)

func TestDiagnosticsRequestsLicenseOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	db := newWebTestDB(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := json.Marshal(&audible.Credentials{
		ADPToken: "synthetic-token", AccessToken: "synthetic-access", ExpiresAt: time.Now().Add(time.Hour),
		DevicePrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
	})
	if err != nil {
		t.Fatal(err)
	}
	account := &database.AudibleAccount{ID: "synthetic-account", DisplayName: "Synthetic account", Marketplace: "us", Credentials: credentials, Enabled: true}
	if err := db.CreateAudibleAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	manager := library.NewAccountManager(ctx, db, nil)
	var licenseRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/licenserequest") {
			if licenseRequests.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"content_license":{"status_code":"Denied","license_denial_reasons":[{"rejectionReason":"CustomerThrottled"}]}}`))
			} else {
				_, _ = w.Write([]byte(`{"content_license":{"status_code":"Granted","content_url":"https://cdn.example.test/book.aax"}}`))
			}
			return
		}
		_, _ = w.Write([]byte(`{"item":{"asin":"B000000001","title":"Synthetic title","content_type":"audiobook"}}`))
	}))
	defer upstream.Close()
	manager.ClientForAccount(account.ID).SetAPIEndpoint(upstream.URL)
	server := &Server{db: db, accounts: manager}
	wantRequests := int32(0)
	for attempt, query := range []string{"", "?check_license=false", "?check_license=anything", "?check_license=true", "?check_license=true", ""} {
		requested := query == "?check_license=true"
		if requested {
			wantRequests++
		}
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/diagnostics/asin/B000000001"+query, nil)
		c.Params = gin.Params{{Key: "asin", Value: "B000000001"}}
		server.handleDiagnosticsASINProbe(c)
		if response.Code != http.StatusOK {
			t.Fatalf("diagnostics status %d: %s", response.Code, response.Body.String())
		}
		var body struct {
			Accounts []diagnosticsAccountProbe `json:"accounts"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Accounts) != 1 || !body.Accounts[0].Found || body.Accounts[0].EntitlementChecked != requested {
			t.Fatalf("diagnostics reported wrong check state: %s", response.Body.String())
		}
		probe := body.Accounts[0]
		if !requested && (probe.Entitled || probe.EntitleError != "") {
			t.Fatal("unchecked lookup reported a license result")
		}
		if requested && wantRequests == 1 && (probe.Entitled || probe.EntitleError == "") {
			t.Fatal("explicit lookup lost license refusal")
		}
		if requested && wantRequests == 2 && (!probe.Entitled || probe.EntitleError != "") {
			t.Fatal("explicit lookup lost license grant")
		}
		if licenseRequests.Load() != wantRequests {
			t.Fatalf("diagnostics sent %d licenses after lookup %d, want %d", licenseRequests.Load(), attempt+1, wantRequests)
		}
	}
}
