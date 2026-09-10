package policy

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateBaseURL enforces the SSRF rules on a configured Redash address.
//
// It returns warnings separately from errors because one common condition
// must not stop the server from starting: a hostname that does not resolve
// is the normal state of an office-only Redash when the VPN happens to be
// down, and refusing to boot then would be worse than failing at call time
// with a message that says so.
func ValidateBaseURL(raw string, allowPrivate bool) (*url.URL, []string, error) {
	var warns []string

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, fmt.Errorf("no URL configured")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Host == "" {
		return nil, nil, fmt.Errorf("URL %q has no host", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, nil, fmt.Errorf("URL %q must not carry a query string or fragment", raw)
	}
	if u.User != nil {
		return nil, nil, fmt.Errorf("URL %q must not embed credentials; put the key in a key file instead", raw)
	}

	host := u.Hostname()
	loopbackName := host == "localhost" || host == "127.0.0.1" || host == "::1"

	switch u.Scheme {
	case "https":
		// ok
	case "http":
		if !loopbackName {
			return nil, nil, fmt.Errorf("URL %q must use https (http is permitted only for localhost)", raw)
		}
	default:
		return nil, nil, fmt.Errorf("URL %q must use https, got scheme %q", raw, u.Scheme)
	}

	u.Path = strings.TrimRight(u.Path, "/")

	ips, err := net.LookupIP(host)
	if err != nil {
		warns = append(warns, fmt.Sprintf(
			"%s does not resolve right now — expected if this Redash is only reachable over VPN or the office network",
			host))
		return u, warns, nil
	}

	for _, ip := range ips {
		if !isPrivateAddr(ip) {
			continue
		}
		if !allowPrivate {
			return nil, nil, fmt.Errorf(
				"%s resolves to the private address %s.\n"+
					"That is normal for a self-hosted or VPN-only Redash, but it is also exactly what an SSRF attempt looks like, so it has to be opted into.\n"+
					"If you reach this Redash over VPN, set REDASH_ALLOW_PRIVATE_ADDRS=true",
				host, ip)
		}
		warns = append(warns, fmt.Sprintf(
			"%s resolves to the private address %s (permitted by REDASH_ALLOW_PRIVATE_ADDRS)", host, ip))
	}
	return u, warns, nil
}

func isPrivateAddr(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified()
}
