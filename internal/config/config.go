// Package config reads configuration from environment variables. There is no
// file-based config: the collector runs as a container, and in Kubernetes a
// ConfigMap already turns into environment variables.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// String reads the NABIZ_<key> environment variable.
func String(key, def string) string {
	if v, ok := os.LookupEnv("NABIZ_" + key); ok && v != "" {
		return v
	}
	return def
}

// Int reads a numeric setting.
func Int(key string, def int) int {
	if v, ok := os.LookupEnv("NABIZ_" + key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Bool treats "1", "true" and "yes" as true.
func Bool(key string, def bool) bool {
	if v, ok := os.LookupEnv("NABIZ_" + key); ok {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

// Duration reads values such as "15s" or "2m".
func Duration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv("NABIZ_" + key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// StringSlice reads a comma-separated list.
func StringSlice(key string, def []string) []string {
	if v, ok := os.LookupEnv("NABIZ_" + key); ok && v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}
