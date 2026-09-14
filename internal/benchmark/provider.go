package benchmark

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// ProviderEnv is a real Usenet provider, read from an operator's .env file.
// Its values are never logged or written to an artifact: only the file's path
// travels on a command line, and the password is scrubbed from what a run
// keeps.
type ProviderEnv struct {
	Host        string
	Port        string
	TLS         bool
	Username    string
	Password    string
	Connections int
}

// Provider .env keys.
const (
	providerHostKey        = "NNTP_HOST"
	providerPortKey        = "NNTP_PORT"
	providerTLSKey         = "NNTP_TLS"
	providerUsernameKey    = "NNTP_USERNAME"
	providerPasswordKey    = "NNTP_PASSWORD"
	providerConnectionsKey = "NNTP_CONNECTIONS"
)

// Address is the provider's host:port.
func (p ProviderEnv) Address() string {
	return net.JoinHostPort(p.Host, p.Port)
}

// LoadProviderEnv reads a provider .env file. It accepts the dotenv subset
// operators actually write -- KEY=VALUE lines, # comments, an optional
// `export`, single or double quotes -- and refuses anything else, including a
// key it does not know, because a misspelt key would otherwise silently fall
// back to a default and measure a different setup than the one written down.
// No error it returns carries a value from the file.
func LoadProviderEnv(path string) (ProviderEnv, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ProviderEnv{}, fmt.Errorf("provider env: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ProviderEnv{}, fmt.Errorf("provider env %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return ProviderEnv{}, fmt.Errorf("provider env %s holds a password but is readable by other users (mode %04o); chmod 600 it", path, info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return ProviderEnv{}, fmt.Errorf("provider env: %w", err)
	}
	values, err := parseDotEnv(contents)
	if err != nil {
		return ProviderEnv{}, fmt.Errorf("provider env %s: %w", path, err)
	}
	return providerFromValues(path, values)
}

func parseDotEnv(contents []byte) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimSpace(strings.TrimPrefix(text, "export "))
		key, value, found := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, fmt.Errorf("line %d is not KEY=VALUE", line)
		}
		switch key {
		case providerHostKey, providerPortKey, providerTLSKey, providerUsernameKey, providerPasswordKey, providerConnectionsKey:
		default:
			return nil, fmt.Errorf("line %d sets unknown key %q", line, key)
		}
		if _, repeated := values[key]; repeated {
			return nil, fmt.Errorf("line %d sets %s a second time", line, key)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("line %d opens a quote for %s it never closes", line, key)
			}
			rest := strings.TrimSpace(value[end+2:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, fmt.Errorf("line %d has text after the quoted value of %s", line, key)
			}
			value = value[1 : end+1]
		} else if comment := strings.Index(value, " #"); comment >= 0 {
			value = strings.TrimSpace(value[:comment])
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func providerFromValues(path string, values map[string]string) (ProviderEnv, error) {
	provider := ProviderEnv{
		Host:     values[providerHostKey],
		Username: values[providerUsernameKey],
		Password: values[providerPasswordKey],
		TLS:      true,
	}
	if provider.Host == "" {
		return ProviderEnv{}, fmt.Errorf("provider env %s must set %s", path, providerHostKey)
	}
	if strings.ContainsAny(provider.Host, " /:@") && net.ParseIP(provider.Host) == nil {
		return ProviderEnv{}, fmt.Errorf("provider env %s: %s must be a bare host name, not a URL", path, providerHostKey)
	}
	if raw, ok := values[providerTLSKey]; ok {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return ProviderEnv{}, fmt.Errorf("provider env %s: %s must be true or false", path, providerTLSKey)
		}
		provider.TLS = parsed
	}
	provider.Port = values[providerPortKey]
	if provider.Port == "" {
		provider.Port = "119"
		if provider.TLS {
			provider.Port = "563"
		}
	}
	if port, err := strconv.Atoi(provider.Port); err != nil || port < 1 || port > 65535 {
		return ProviderEnv{}, fmt.Errorf("provider env %s: %s must be a port number", path, providerPortKey)
	}
	if (provider.Username == "") != (provider.Password == "") {
		return ProviderEnv{}, fmt.Errorf("provider env %s must set both %s and %s, or neither", path, providerUsernameKey, providerPasswordKey)
	}
	if provider.Password != "" && len(provider.Password) < minimumScrubbedSecret {
		return ProviderEnv{}, fmt.Errorf("provider env %s: %s is shorter than %d bytes and could not be scrubbed from run evidence", path, providerPasswordKey, minimumScrubbedSecret)
	}
	// The connection count is required rather than defaulted: it is part of
	// what the lane measures, and a provider account caps it.
	raw := values[providerConnectionsKey]
	connections, err := strconv.Atoi(raw)
	if raw == "" || err != nil || connections < 1 {
		return ProviderEnv{}, fmt.Errorf("provider env %s must set %s to a positive connection count within the account's limit", path, providerConnectionsKey)
	}
	provider.Connections = connections
	return provider, nil
}
