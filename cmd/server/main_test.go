package main

import (
	"context"
	"testing"

	"github.com/mstrhakr/audplexus/internal/auth"
	"github.com/mstrhakr/audplexus/internal/database"
)

func TestSeedAuthDefaults_DisabledByDefault(t *testing.T) {
	t.Setenv("AUDPLEXUS_ADMIN_USERNAME", "")
	t.Setenv("AUDPLEXUS_ADMIN_PASSWORD", "")

	db := database.NewStubDB()
	seedAuthDefaults(context.Background(), db)

	if got, _ := db.GetSetting(context.Background(), auth.SettingKeyAuthMethod); got != string(auth.AuthMethodNone) {
		t.Fatalf("auth_method = %q, want %q", got, auth.AuthMethodNone)
	}
	if got, _ := db.GetSetting(context.Background(), auth.SettingKeyAuthRequired); got != string(auth.AuthRequiredDisabledForLocalhost) {
		t.Fatalf("auth_required = %q, want %q", got, auth.AuthRequiredDisabledForLocalhost)
	}
}

func TestSeedAuthDefaults_UsesEnvBootstrapWhenProvided(t *testing.T) {
	t.Setenv("AUDPLEXUS_ADMIN_USERNAME", "admin")
	t.Setenv("AUDPLEXUS_ADMIN_PASSWORD", "ChangeMe!123")

	db := database.NewStubDB()
	seedAuthDefaults(context.Background(), db)

	if got, _ := db.GetSetting(context.Background(), auth.SettingKeyAuthMethod); got != string(auth.AuthMethodForms) {
		t.Fatalf("auth_method = %q, want %q", got, auth.AuthMethodForms)
	}
	if got, _ := db.GetSetting(context.Background(), auth.SettingKeyAuthRequired); got != string(auth.AuthRequiredEnabled) {
		t.Fatalf("auth_required = %q, want %q", got, auth.AuthRequiredEnabled)
	}
}
