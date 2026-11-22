package main

import "testing"

func TestCodeFromText(t *testing.T) {
	cases := map[string]string{
		`1:"NEXT_REDIRECT;replace;appie://login-exit?code=abc123;307;"`:   "abc123",
		`{"url":"appie:\/\/login-exit?state=x&code=def456"}`:              "def456",
		`{"pageProps":{"__N_REDIRECT":"appie://login-exit?code=ghi789"}}`: "ghi789",
		`<a href="https://login.ah.nl/ingelogd">no redirect here</a>`:     "",
	}
	for in, want := range cases {
		if got := codeFromText(in); got != want {
			t.Errorf("codeFromText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewInstallationID(t *testing.T) {
	id, err := newInstallationID()
	if err != nil || len(id) != 36 || id[14] != '4' {
		t.Fatalf("newInstallationID() = %q, %v", id, err)
	}
}
