package wokey

import "net/http"

// RemapError is Wokey's rule on top of the core's error mapping
// (upstream.Errors): a 403 request_blocked or model_denylisted is a problem
// with the request, not the key, so it becomes 400 and does not cool the key
// down. Account-level 403s keep their status. Messages are kept as they are.
func (a *Adapter) RemapError(status int, code, message string) (int, string) {
	if status == http.StatusForbidden && (code == "request_blocked" || code == "model_denylisted") {
		return http.StatusBadRequest, message
	}
	return 0, message
}
