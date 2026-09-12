// Package config, ortam değişkeninden yapılandırma okur. Dosya tabanlı config
// yok: collector bir container olarak çalışacak ve k8s'te ConfigMap zaten
// ortam değişkenine dönüşüyor.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// String, NABIZ_<key> ortam değişkenini okur.
func String(key, def string) string {
	if v, ok := os.LookupEnv("NABIZ_" + key); ok && v != "" {
		return v
	}
	return def
}

// Int, sayısal ayar okur.
func Int(key string, def int) int {
	if v, ok := os.LookupEnv("NABIZ_" + key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Bool, "1", "true", "yes" değerlerini doğru kabul eder.
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

// Duration, "15s", "2m" gibi değerleri okur.
func Duration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv("NABIZ_" + key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// StringSlice, virgülle ayrılmış listeyi okur.
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
