package identity

import "strings"

// maxEmailLength is the maximum length of an e-mail address (RFC 5321 path limit).
const maxEmailLength = 320

// NormalizeEmail trims and lowercases an e-mail address and checks its basic form
// (local part, "@", a domain with a dot, no whitespace). Delivery is not verified.
func NormalizeEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	local, domain, ok := strings.Cut(email, "@")
	valid := ok && local != "" && strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") &&
		!strings.HasSuffix(domain, ".") && !strings.Contains(domain, "@") &&
		!strings.ContainsAny(email, " \t\r\n") && len(email) <= maxEmailLength
	return email, valid
}
