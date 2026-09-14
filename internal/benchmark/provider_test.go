package benchmark

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeProviderEnv(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadProviderEnvReadsTheDotenvSubset(t *testing.T) {
	path := writeProviderEnv(t, `# provider
export NNTP_HOST=news.example.net
NNTP_USERNAME="user name"
NNTP_PASSWORD='p#ss word'   # trailing comment
NNTP_CONNECTIONS=20 # within the plan

`, 0o600)
	provider, err := LoadProviderEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	want := ProviderEnv{Host: "news.example.net", Port: "563", TLS: true, Username: "user name", Password: "p#ss word", Connections: 20}
	if provider != want {
		t.Fatalf("provider = %#v, want %#v", provider, want)
	}
	if provider.Address() != "news.example.net:563" {
		t.Fatalf("address = %q", provider.Address())
	}
}

func TestLoadProviderEnvRefusesWhatItCannotTrust(t *testing.T) {
	const base = "NNTP_HOST=news.example.net\nNNTP_USERNAME=user\nNNTP_PASSWORD=secret-value\nNNTP_CONNECTIONS=8\n"
	cases := map[string]struct {
		contents string
		want     string
	}{
		"unknown key":        {base + "NNTP_CONECTIONS=9\n", "unknown key"},
		"repeated key":       {base + "NNTP_HOST=other.example.net\n", "second time"},
		"no host":            {"NNTP_CONNECTIONS=8\n", "must set NNTP_HOST"},
		"url host":           {strings.Replace(base, "news.example.net", "nntps://news.example.net", 1), "bare host"},
		"no connections":     {"NNTP_HOST=news.example.net\n", "NNTP_CONNECTIONS"},
		"password only":      {"NNTP_HOST=news.example.net\nNNTP_PASSWORD=secret-value\nNNTP_CONNECTIONS=8\n", "both"},
		"short password":     {strings.Replace(base, "secret-value", "abc", 1), "shorter"},
		"bad tls":            {base + "NNTP_TLS=maybe\n", "true or false"},
		"unterminated quote": {base + "NNTP_PORT=\"563\n", "never closes"},
		"not assignment":     {base + "garbage\n", "not KEY=VALUE"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadProviderEnv(writeProviderEnv(t, tc.contents, 0o600))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("error %q leaks the password", err)
			}
		})
	}
}

func TestLoadProviderEnvRefusesAFileOtherUsersCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not describe a Windows ACL")
	}
	path := writeProviderEnv(t, "NNTP_HOST=news.example.net\nNNTP_CONNECTIONS=8\n", 0o644)
	if _, err := LoadProviderEnv(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("error = %v, want a refusal of a world-readable provider env", err)
	}
}

func TestLoadProviderEnvDefaultsThePlaintextPortWhenTLSIsOff(t *testing.T) {
	provider, err := LoadProviderEnv(writeProviderEnv(t, "NNTP_HOST=news.example.net\nNNTP_TLS=false\nNNTP_CONNECTIONS=4\n", 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if provider.TLS || provider.Port != "119" {
		t.Fatalf("provider = %#v, want plaintext on 119", provider)
	}
}
