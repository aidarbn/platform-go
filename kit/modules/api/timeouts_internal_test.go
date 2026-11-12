package api

import (
	"testing"
	"time"
)

// A Config built by hand is never open to slowloris and does not keep idle
// connections forever; the other timeouts stay off unless set.
func TestHTTPTimeoutDefaults(t *testing.T) {
	for name, tc := range map[string]struct {
		in                        Config
		header, read, write, idle time.Duration
	}{
		"zero":     {Config{}, DefaultHTTPReadHeaderTimeout, 0, 0, DefaultHTTPIdleTimeout},
		"negative": {Config{HTTPReadHeaderTimeout: -1, HTTPIdleTimeout: -1}, DefaultHTTPReadHeaderTimeout, 0, 0, -1},
		"set": {
			Config{HTTPReadHeaderTimeout: 5 * time.Second, HTTPReadTimeout: 30 * time.Second, HTTPWriteTimeout: time.Minute, HTTPIdleTimeout: time.Minute},
			5 * time.Second, 30 * time.Second, time.Minute, time.Minute,
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := New(tc.in).cfg
			if c.HTTPReadHeaderTimeout != tc.header || c.HTTPReadTimeout != tc.read ||
				c.HTTPWriteTimeout != tc.write || c.HTTPIdleTimeout != tc.idle {
				t.Errorf("%v %v %v %v", c.HTTPReadHeaderTimeout, c.HTTPReadTimeout, c.HTTPWriteTimeout, c.HTTPIdleTimeout)
			}
		})
	}
}
