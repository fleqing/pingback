package keychain_test

import (
	"errors"
	"reflect"
	"testing"

	"pingback/internal/keychain"
)

func TestPassword_ReadsGenericPasswordByServiceAndProfile(t *testing.T) {
	var gotName string
	var gotArgs []string
	src := keychain.Source{Exec: func(name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("s3cret\n"), nil
	}}

	pw, ok := src.Password("main")

	if !ok || pw != "s3cret" {
		t.Fatalf("Password = %q, %v; want %q, true", pw, ok, "s3cret")
	}
	if gotName != "security" {
		t.Errorf("command = %q, want security", gotName)
	}
	want := []string{"find-generic-password", "-s", "pingback", "-a", "main", "-w"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

func TestPassword_MissingEntryReportsNotFound(t *testing.T) {
	src := keychain.Source{Exec: func(string, ...string) ([]byte, error) {
		return nil, errors.New("exit status 44")
	}}

	if pw, ok := src.Password("main"); ok || pw != "" {
		t.Fatalf("Password = %q, %v; want empty, false", pw, ok)
	}
}
