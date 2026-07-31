package middleware

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const DefaultCompressionThreshold = 1024

type CompressionConfig struct {
	MinSize       int
	ExcludePaths  []string
	PreferDeflate bool
}

type compressionResponseWriter struct {
	http.ResponseWriter
	body        bytes.Buffer
	statusCode  int
	wroteHeader bool
}

func (w *compressionResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.statusCode = code
	w.wroteHeader = true
}

func (w *compressionResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(p)
}

func Compression(cfg CompressionConfig) func(http.Handler) http.Handler {
	if cfg.MinSize <= 0 {
		cfg.MinSize = DefaultCompressionThreshold
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if compressionExcluded(r, cfg.ExcludePaths) {
				next.ServeHTTP(w, r)
				return
			}

			appendVary(w.Header(), "Accept-Encoding")
			encoding := negotiateEncoding(r.Header.Get("Accept-Encoding"), cfg.PreferDeflate)
			cw := &compressionResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(cw, r)

			body := cw.body.Bytes()
			if shouldCompress(cw.Header(), cw.statusCode, len(body), cfg.MinSize, encoding) {
				cw.Header().Del("Content-Length")
				cw.Header().Set("Content-Encoding", encoding)
				w.WriteHeader(cw.statusCode)
				_ = writeCompressed(w, body, encoding)
				return
			}

			if cw.wroteHeader {
				w.WriteHeader(cw.statusCode)
			}
			if len(body) > 0 && bodyAllowed(cw.statusCode) {
				_, _ = w.Write(body)
			}
		})
	}
}

func NewCompression() func(http.Handler) http.Handler {
	return Compression(CompressionConfig{
		MinSize: DefaultCompressionThreshold,
		ExcludePaths: []string{
			"/v1/events/stream",
			"/ws",
			"/graphql",
		},
	})
}

func compressionExcluded(r *http.Request, paths []string) bool {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return true
	}
	for _, prefix := range paths {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return true
		}
	}
	return false
}

func shouldCompress(h http.Header, statusCode, bodySize, minSize int, encoding string) bool {
	if encoding == "" || bodySize <= minSize || h.Get("Content-Encoding") != "" || !bodyAllowed(statusCode) {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func bodyAllowed(statusCode int) bool {
	return statusCode >= http.StatusOK && statusCode != http.StatusNoContent && statusCode != http.StatusNotModified
}

func writeCompressed(w io.Writer, body []byte, encoding string) error {
	switch encoding {
	case "deflate":
		zw := zlib.NewWriter(w)
		if _, err := zw.Write(body); err != nil {
			_ = zw.Close()
			return err
		}
		return zw.Close()
	default:
		zw := gzip.NewWriter(w)
		if _, err := zw.Write(body); err != nil {
			_ = zw.Close()
			return err
		}
		return zw.Close()
	}
}

func negotiateEncoding(header string, preferDeflate bool) string {
	if header == "" {
		return ""
	}

	type candidate struct {
		q    float64
		seen bool
	}
	var gzipEncoding, deflateEncoding candidate
	for _, part := range strings.Split(header, ",") {
		token, q := parseEncoding(part)
		switch token {
		case "gzip":
			gzipEncoding = candidate{q: q, seen: true}
		case "deflate":
			deflateEncoding = candidate{q: q, seen: true}
		case "*":
			if !gzipEncoding.seen {
				gzipEncoding = candidate{q: q, seen: true}
			}
			if !deflateEncoding.seen {
				deflateEncoding = candidate{q: q, seen: true}
			}
		}
	}

	if preferDeflate {
		if deflateEncoding.q > 0 && deflateEncoding.q >= gzipEncoding.q {
			return "deflate"
		}
		if gzipEncoding.q > 0 {
			return "gzip"
		}
		return ""
	}
	if gzipEncoding.q > 0 && gzipEncoding.q >= deflateEncoding.q {
		return "gzip"
	}
	if deflateEncoding.q > 0 {
		return "deflate"
	}
	return ""
}

func parseEncoding(part string) (string, float64) {
	fields := strings.Split(part, ";")
	token := strings.ToLower(strings.TrimSpace(fields[0]))
	if token == "" {
		return "", 0
	}

	q := 1.0
	for _, field := range fields[1:] {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok || !strings.EqualFold(key, "q") {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return token, 0
		}
		if parsed < 0 {
			parsed = 0
		}
		if parsed > 1 {
			parsed = 1
		}
		q = parsed
	}
	return token, q
}

func appendVary(h http.Header, value string) {
	for _, existing := range h.Values("Vary") {
		for _, part := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	if current := h.Get("Vary"); current != "" {
		h.Set("Vary", current+", "+value)
		return
	}
	h.Set("Vary", value)
}
