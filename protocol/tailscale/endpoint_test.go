//go:build with_gvisor

package tailscale

import (
	"os"
	"testing"
)

func TestSetTemporaryEnvRestoresMissingValue(t *testing.T) {
	t.Setenv("SING_BOX_TEST_TEMP_ENV", "")
	if err := os.Unsetenv("SING_BOX_TEST_TEMP_ENV"); err != nil {
		t.Fatal(err)
	}
	restore, err := setTemporaryEnv("SING_BOX_TEST_TEMP_ENV", "true")
	if err != nil {
		t.Fatal(err)
	}
	if value := os.Getenv("SING_BOX_TEST_TEMP_ENV"); value != "true" {
		t.Fatalf("expected temporary value true, got %q", value)
	}
	restore()
	if _, exists := os.LookupEnv("SING_BOX_TEST_TEMP_ENV"); exists {
		t.Fatal("expected environment variable to be unset after restore")
	}
}

func TestSetTemporaryEnvRestoresExistingValue(t *testing.T) {
	t.Setenv("SING_BOX_TEST_TEMP_ENV", "old")
	restore, err := setTemporaryEnv("SING_BOX_TEST_TEMP_ENV", "true")
	if err != nil {
		t.Fatal(err)
	}
	if value := os.Getenv("SING_BOX_TEST_TEMP_ENV"); value != "true" {
		t.Fatalf("expected temporary value true, got %q", value)
	}
	restore()
	if value := os.Getenv("SING_BOX_TEST_TEMP_ENV"); value != "old" {
		t.Fatalf("expected restored value old, got %q", value)
	}
}
