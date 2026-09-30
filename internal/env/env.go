// Package env reads service configuration from environment variables.
package env

import (
	"log"
	"os"
	"strconv"
)

func String(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func Float(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Fatalf("%s=%q: %v", name, v, err)
	}
	return f
}

func Must(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s must be set", name)
	}
	return v
}
