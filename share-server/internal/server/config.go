package server

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultListen       = "127.0.0.1:8080"
	defaultPublicOrigin = "http://127.0.0.1:8080"
	defaultDataDir      = "./data"
	defaultTTL          = 7 * 24 * time.Hour
	maxTTL              = 365 * 24 * time.Hour
	maxTokenBytes       = 4096
)

// Config is the runtime configuration for the share service.
type Config struct {
	Listen       string
	PublicOrigin string
	Token        string
	DataDir      string
	TTL          time.Duration
	FFmpegPath   string
}

// LoadConfig reads the service configuration from getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Listen:       strings.TrimSpace(getenv("SHARE_LISTEN")),
		PublicOrigin: strings.TrimSpace(getenv("SHARE_PUBLIC_ORIGIN")),
		DataDir:      strings.TrimSpace(getenv("SHARE_DATA_DIR")),
		TTL:          defaultTTL,
	}
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return Config{}, fmt.Errorf("SHARE_LISTEN must be host:port: %w", err)
	}
	if cfg.PublicOrigin == "" {
		cfg.PublicOrigin = defaultPublicOrigin
	}
	origin, err := normalizeOrigin(cfg.PublicOrigin)
	if err != nil {
		return Config{}, fmt.Errorf("SHARE_PUBLIC_ORIGIN: %w", err)
	}
	cfg.PublicOrigin = origin
	if cfg.Token, err = loadToken(getenv); err != nil {
		return Config{}, err
	}
	if raw := strings.TrimSpace(getenv("SHARE_TTL")); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("SHARE_TTL: %w", err)
		}
		if ttl <= 0 || ttl > maxTTL {
			return Config{}, fmt.Errorf("SHARE_TTL must be greater than zero and at most %s", maxTTL)
		}
		cfg.TTL = ttl
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir
	}
	dir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("SHARE_DATA_DIR: %w", err)
	}
	if err := ensureDir(dir); err != nil {
		return Config{}, fmt.Errorf("SHARE_DATA_DIR: %w", err)
	}
	cfg.DataDir = dir
	return cfg, nil
}

func normalizeOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("must be an absolute URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && !(scheme == "http" && hostIsLoopback(parsed.Hostname())) {
		return "", errors.New("must use HTTPS unless the host is loopback")
	}
	if parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("must not contain credentials, a path, or a query")
	}
	parsed.Scheme = scheme
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func loadToken(getenv func(string) string) (string, error) {
	inline := strings.TrimSpace(getenv("SHARE_TOKEN"))
	file := strings.TrimSpace(getenv("SHARE_TOKEN_FILE"))
	if inline != "" && file != "" {
		return "", errors.New("set only one of SHARE_TOKEN and SHARE_TOKEN_FILE")
	}
	token := inline
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read SHARE_TOKEN_FILE: %w", err)
		}
		if len(data) > maxTokenBytes {
			return "", errors.New("SHARE_TOKEN_FILE is too large")
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		return "", errors.New("SHARE_TOKEN or SHARE_TOKEN_FILE is required")
	}
	if len(token) > maxTokenBytes {
		return "", errors.New("share token is too large")
	}
	for _, r := range token {
		if r <= ' ' || r == 0x7f {
			return "", errors.New("share token must not contain whitespace or control characters")
		}
	}
	return token, nil
}

func ensureDir(dir string) error {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return errors.New("path is not a directory")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

func hostIsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func listenIsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	return hostIsLoopback(host)
}
