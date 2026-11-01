package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

var capSeq atomic.Uint64

var secretHeader = map[string]bool{"authorization": true, "cookie": true, "x-internal-key": true, "set-cookie": true, "proxy-authorization": true}

// capture handles every request that matches no route. It is the server half of the SSL-trace
// approach: once a HOME host is redirected here, the first real client request is written to disk
// (tokens redacted) and answered 501, so the protocol can be written from evidence instead of guesses.
func (a *App) capture(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	id := time.Now().UTC().Format("20060102T150405.000") + "-" + itoa(capSeq.Add(1))
	if a.CaptureDir != "" {
		hdr := map[string][]string{}
		for k, v := range r.Header {
			if secretHeader[strings.ToLower(k)] {
				hdr[k] = []string{"<redacted len=" + itoa(uint64(len(strings.Join(v, "")))) + ">"}
			} else {
				hdr[k] = v
			}
		}
		rec := map[string]any{"id": id, "time": time.Now().UTC(), "host": r.Host, "method": r.Method, "uri": r.URL.RequestURI(),
			"proto": r.Proto, "remote": r.RemoteAddr, "headers": hdr, "body": body}
		if b, err := json.MarshalIndent(rec, "", " "); err == nil {
			_ = os.MkdirAll(a.CaptureDir, 0o750)
			_ = os.WriteFile(filepath.Join(a.CaptureDir, id+".json"), b, 0o640)
		}
	}
	// the path and host go to the log either way: that alone names the missing endpoints
	logf("unhandled %s %s%s (capture %s)", r.Method, r.Host, r.URL.Path, id)
	reply(w, 501, map[string]any{"error": "not_implemented", "capture": id})
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
