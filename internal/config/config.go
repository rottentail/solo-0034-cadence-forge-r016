package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Root, Address, FFmpeg, FFprobe string
	Workers, QueueLimit            int
	UploadLimit                    int64
	// BurnFont is the libass font family used for optional burned captions.
	// ExplicitBurnFont records whether the operator chose it: the built-in
	// default is only safe for scripts it actually covers, so CJK burn output
	// requires an explicit choice instead of silently rendering missing glyphs.
	BurnFont         string
	ExplicitBurnFont bool
}

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func Load() (Config, error) {
	c := Config{Root: env("FORGE_ROOT", "var/cadence-forge"), Address: env("FORGE_ADDR", "127.0.0.1:8734"), FFmpeg: env("FFMPEG_BIN", "ffmpeg"), FFprobe: env("FFPROBE_BIN", "ffprobe"), Workers: 2, QueueLimit: 16, UploadLimit: 64 << 20, BurnFont: "DejaVu Sans"}
	if value := os.Getenv("FORGE_BURN_FONT"); value != "" {
		if strings.ContainsAny(value, ",:'\"[])") || strings.Contains(value, "\\") {
			return c, fmt.Errorf("FORGE_BURN_FONT contains filtergraph metacharacters")
		}
		c.BurnFont = value
		c.ExplicitBurnFont = true
	}
	if value := os.Getenv("FORGE_WORKERS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 8 {
			return c, fmt.Errorf("FORGE_WORKERS must be 1..8")
		}
		c.Workers = n
	}
	return c, nil
}
