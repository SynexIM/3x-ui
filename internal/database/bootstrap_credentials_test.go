package database

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
)

func TestManagedBootstrapCredentialIsWrittenOnce(t *testing.T) {
	dbFolder := t.TempDir()
	t.Setenv("IPLINE_MANAGED", "1")
	t.Setenv("XUI_DB_FOLDER", dbFolder)
	t.Setenv("XUI_DB_TYPE", "sqlite")
	if err := InitDB(filepath.Join(dbFolder, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	credentialPath := filepath.Join(dbFolder, bootstrapCredentialsFilename)
	info, err := os.Stat(credentialPath)
	if err != nil {
		t.Fatalf("stat bootstrap credential: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("bootstrap credential mode = %o, want 600", got)
	}
	contents, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatalf("read bootstrap credential: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(contents, &payload); err != nil {
		t.Fatalf("decode bootstrap credential: %v", err)
	}
	if len(payload) != 1 || payload["token"] == "" {
		t.Fatalf("bootstrap credential = %#v, want only a token", payload)
	}

	var token model.ApiToken
	if err := db.Where("name = ?", managedBootstrapTokenName).Take(&token).Error; err != nil {
		t.Fatalf("load managed API token: %v", err)
	}
	if token.Namespaces != managedBootstrapNamespace {
		t.Fatalf("bootstrap token namespaces = %q, want %q", token.Namespaces, managedBootstrapNamespace)
	}
	if token.Token != crypto.HashTokenSHA256(payload["token"]) {
		t.Fatal("bootstrap token is not stored as the credential file's hash")
	}

	if err := os.Remove(credentialPath); err != nil {
		t.Fatalf("remove returned credential: %v", err)
	}
	if err := CloseDB(); err != nil {
		t.Fatalf("CloseDB: %v", err)
	}
	if err := InitDB(filepath.Join(dbFolder, "x-ui.db")); err != nil {
		t.Fatalf("restart InitDB: %v", err)
	}
	if _, err := os.Stat(credentialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart rotated credential: %v", err)
	}
}
