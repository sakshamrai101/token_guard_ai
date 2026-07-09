package proxy

import (
	"net/http"
	"strings"
)


var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailers":            {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

var internalHeaders = map[string]struct{}{
	"x-budget-bucket-id": {},
	"x-request-id":       {},
}

func SanitizeRequestHeaders(h http.Header, upstreamHost string) {
	h.Set("Host", upstreamHost)

	connTokens := parseConnectionTokens(h.Get("Connection"))

	for name := range h {
		lower := strings.ToLower(name)
		if _, ok := hopByHopHeaders[lower]; ok {
			h.Del(name)
			continue
		}
		if _, ok := internalHeaders[lower]; ok {
			h.Del(name)
		}
	}

	for _, token := range connTokens {
		h.Del(token)
	}
}

func parseConnectionTokens(conn string) []string {
	if conn == "" {
		return nil
	}
	tokens := strings.Split(conn, ",")
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if t := strings.TrimSpace(token); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func SanitizeResponseHeaders(h http.Header) {
	for name := range h {
		if _, ok := hopByHopHeaders[strings.ToLower(name)]; ok {
			h.Del(name)
		}
	}
}
