package web

// Helper test utilities for web package tests
import (
	"testing"

	"github.com/mstrhakr/audplexus/internal/database"
)

// newTestDB creates a StubDB for web tests
func newTestDB(t *testing.T) database.Database {
	return database.NewStubDB()
}
