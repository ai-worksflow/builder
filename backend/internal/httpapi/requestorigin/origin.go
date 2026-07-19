package requestorigin

import (
	"net/http"
	"net/url"
	"strings"
)

// Same reports whether the browser Origin is the effective public origin of
// the current request. Same-origin traffic is not CORS and must not require a
// deployment-specific allowlist entry merely because the public host changed.
// Reverse proxies are expected to preserve Host and overwrite
// X-Forwarded-Proto, as the bundled Nginx configuration does.
func Same(request *http.Request, origin string) bool {
	browserOrigin, ok := canonicalOrigin(origin)
	if !ok || request == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(request.Host))
	if host == "" || strings.ContainsAny(host, "/\\@") {
		return false
	}
	scheme := effectiveScheme(request)
	if scheme == "" {
		return false
	}
	return browserOrigin == scheme+"://"+host
}

func canonicalOrigin(value string) (string, bool) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), true
}

func effectiveScheme(request *http.Request) string {
	forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0])
	forwarded = strings.ToLower(forwarded)
	if forwarded == "http" || forwarded == "https" {
		return forwarded
	}
	if request.TLS != nil {
		return "https"
	}
	if request.URL != nil && (request.URL.Scheme == "http" || request.URL.Scheme == "https") {
		return strings.ToLower(request.URL.Scheme)
	}
	return "http"
}
