package library

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
	audible "github.com/mstrhakr/go-audible"
)

func syncLicenseTestCredentials(t *testing.T) *audible.Credentials {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &audible.Credentials{
		ADPToken: "synthetic-token", AccessToken: "synthetic-access", CustomerID: "synthetic-customer",
		ExpiresAt:        time.Now().Add(time.Hour),
		DevicePrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
	}
}

func TestSyncRefreshesOneThousandTitlesWithoutRequestingLicenses(t *testing.T) {
	ctx := context.Background()
	service := newTestSyncService(t)
	items := make([]map[string]string, 1000)
	for i := range items {
		items[i] = map[string]string{"asin": fmt.Sprintf("B%09d", i), "title": fmt.Sprintf("Synthetic title %d", i), "content_type": "audiobook"}
	}
	body, err := json.Marshal(map[string]any{"items": items, "total_results": len(items)})
	if err != nil {
		t.Fatal(err)
	}
	var licenseRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/licenserequest") {
			licenseRequests.Add(1)
			_, _ = w.Write([]byte(`{"content_license":{"status_code":"Denied"}}`))
			return
		}
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	client := audible.NewClient(audible.MarketplaceUS)
	client.SetAPIEndpoint(upstream.URL)
	client.SetCredentials(syncLicenseTestCredentials(t))
	service.client = client
	for pass := 0; pass < 2; pass++ {
		added, err := service.doAudibleSync(ctx, &database.SyncHistory{})
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if pass == 0 {
			want = len(items)
		}
		if added != want {
			t.Fatalf("sync %d added %d titles, want %d", pass+1, added, want)
		}
		if licenseRequests.Load() != 0 {
			t.Fatalf("sync %d made %d license requests", pass+1, licenseRequests.Load())
		}
		if pass == 0 {
			fresh, _ := service.db.GetBookByASIN(ctx, "B000000002")
			if fresh.Status != database.BookStatusNew {
				t.Fatalf("new library item status = %s", fresh.Status)
			}
			// Refresh must preserve both a saved file and a historical denial.
			complete, _ := service.db.GetBookByASIN(ctx, "B000000000")
			complete.Status = database.BookStatusComplete
			complete.FilePath = "/library/saved.m4b"
			complete.FileSize = 123
			if err := service.db.UpsertBook(ctx, complete); err != nil {
				t.Fatal(err)
			}
			denied, _ := service.db.GetBookByASIN(ctx, "B000000001")
			denied.Status = database.BookStatusUnavailable
			denied.UnavailableReason = "historical denial"
			if err := service.db.UpsertBook(ctx, denied); err != nil {
				t.Fatal(err)
			}
		}
	}
	if licenseRequests.Load() != 0 {
		t.Fatalf("sync requested %d licenses, want zero", licenseRequests.Load())
	}
	saved, _ := service.db.GetBookByASIN(ctx, "B000000000")
	denied, _ := service.db.GetBookByASIN(ctx, "B000000001")
	if saved.Status != database.BookStatusComplete || saved.FilePath != "/library/saved.m4b" || saved.FileSize != 123 {
		t.Fatalf("sync changed saved file metadata: %+v", saved)
	}
	if denied.Status != database.BookStatusUnavailable || denied.UnavailableReason != "historical denial" {
		t.Fatalf("sync changed historical denial: %+v", denied)
	}
}
