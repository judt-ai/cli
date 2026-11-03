package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/judt-ai/cli/internal/config"
)

const releasesURL = "https://api.github.com/repos/judt-ai/cli/releases/latest"

type updateCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// startUpdateCheck looks up the latest release at most once a day, in the
// background, and returns a func that prints one line when a newer version exists.
// It stays quiet with --json, outside a terminal, in a dev build, or when
// JUDT_NO_UPDATE_CHECK is set.
func (s *state) startUpdateCheck() func() {
	if os.Getenv("JUDT_NO_UPDATE_CHECK") != "" || s.version == "dev" {
		return func() {}
	}
	dir, err := config.Dir()
	if err != nil {
		return func() {}
	}
	path := filepath.Join(dir, "update-check.json")
	var cache updateCache
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	done := make(chan struct{})
	if time.Since(cache.CheckedAt) > 24*time.Hour {
		go func() {
			defer close(done)
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(releasesURL)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var release struct {
				TagName string `json:"tag_name"`
			}
			if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&release) != nil {
				return
			}
			cache = updateCache{CheckedAt: time.Now(), Latest: strings.TrimPrefix(release.TagName, "v")}
			if data, err := json.Marshal(cache); err == nil {
				_ = os.MkdirAll(dir, 0o700)
				_ = os.WriteFile(path, data, 0o600)
			}
		}()
	} else {
		close(done)
	}
	return func() {
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
			return
		}
		if s.jsonOut || !s.stdoutTTY || cache.Latest == "" || !newer(cache.Latest, s.version) {
			return
		}
		fmt.Fprintf(s.errOut, "\njudt %s is out (you have %s). To update: %s\n", cache.Latest, s.version, upgradeCommand())
	}
}

// newer reports whether version a is later than b (both X.Y.Z).
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.SplitN(pa[i], "-", 2)[0])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.SplitN(pb[i], "-", 2)[0])
		}
		if x != y {
			return x > y
		}
	}
	return false
}
