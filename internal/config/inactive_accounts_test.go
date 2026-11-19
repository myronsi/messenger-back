package config

import "testing"

func TestInactiveAccountDeletionIsOffByDefault(t *testing.T) {
	cfg, err := LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Inactive.Deletion {
		t.Fatal("inactive account deletion must be off unless INACTIVE_ACCOUNT_DELETION=true")
	}
	env := validEnv()
	env["INACTIVE_ACCOUNT_DELETION"] = "true"
	if cfg, err = LoadFrom(env); err != nil || !cfg.Inactive.Deletion {
		t.Fatalf("INACTIVE_ACCOUNT_DELETION=true: %v %v", cfg.Inactive.Deletion, err)
	}
}
