package mediaserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestPlexAddToCollectionUsesPost(t *testing.T) {
	t.Parallel()

	var (
		mu         sync.Mutex
		methods    []string
		paths      []string
		uris       []string
		queryTokens []string
		headerTokens []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		uris = append(uris, r.URL.Query().Get("uri"))
		queryTokens = append(queryTokens, r.URL.Query().Get("X-Plex-Token"))
		headerTokens = append(headerTokens, r.Header.Get("X-Plex-Token"))
		mu.Unlock()
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := &PlexBackend{clientID: "test-client"}
	if err := p.addToCollection(context.Background(), server.URL, "secret", "123", "server://machine/com.plexapp.plugins.library/library/metadata/456"); err != nil {
		t.Fatalf("addToCollection() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 1 || methods[0] != http.MethodPost {
		t.Fatalf("methods = %v, want [%s]", methods, http.MethodPost)
	}
	if len(paths) != 1 || paths[0] != "/library/collections/123/items" {
		t.Fatalf("paths = %v", paths)
	}
	if len(uris) != 1 || uris[0] != "server://machine/com.plexapp.plugins.library/library/metadata/456" {
		t.Fatalf("uris = %v", uris)
	}
	if len(queryTokens) != 1 || queryTokens[0] != "secret" {
		t.Fatalf("queryTokens = %v", queryTokens)
	}
	if len(headerTokens) != 1 || headerTokens[0] != "secret" {
		t.Fatalf("headerTokens = %v", headerTokens)
	}
}

func TestPlexAddToCollectionFallsBackToPut(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		methods []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()

		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("bad request"))
		case http.MethodPut:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	p := &PlexBackend{clientID: "test-client"}
	if err := p.addToCollection(context.Background(), server.URL, "secret", "123", "server://machine/com.plexapp.plugins.library/library/metadata/456"); err != nil {
		t.Fatalf("addToCollection() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodPut {
		t.Fatalf("methods = %v, want [%s %s]", methods, http.MethodPost, http.MethodPut)
	}
}