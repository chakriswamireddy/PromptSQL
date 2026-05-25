package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
)

// privateRanges are the IP ranges that must never be contacted.
var privateRanges = []net.IPNet{
	parseCIDR("10.0.0.0/8"),
	parseCIDR("172.16.0.0/12"),
	parseCIDR("192.168.0.0/16"),
	parseCIDR("127.0.0.0/8"),
	parseCIDR("169.254.0.0/16"), // link-local
	parseCIDR("::1/128"),
	parseCIDR("fc00::/7"),
	parseCIDR("fe80::/10"),
}

func parseCIDR(s string) net.IPNet {
	_, n, _ := net.ParseCIDR(s)
	return *n
}

func isPrivateIP(ip net.IP) bool {
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// validateWebhookURL checks that the URL is HTTPS and resolves to a public IP.
// Returns the first resolved IP for pinning (anti-DNS-rebinding).
func validateWebhookURL(ctx context.Context, rawURL string) (net.IP, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("webhook URL must use HTTPS")
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing host in webhook URL")
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("DNS resolution failed: %w", err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses resolved for %s", host)
	}

	ip := addrs[0].IP
	if isPrivateIP(ip) {
		metricSSRFBlocked.Inc()
		return nil, fmt.Errorf("SSRF: resolved IP %s is in a private range", ip)
	}

	// DNS rebinding defense: re-resolve and verify consistency.
	addrs2, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs2) == 0 {
		return nil, fmt.Errorf("DNS re-resolution failed")
	}
	ip2 := addrs2[0].IP
	if !ip.Equal(ip2) {
		metricSSRFBlocked.Inc()
		return nil, fmt.Errorf("SSRF: DNS rebinding detected for %s", host)
	}

	return ip, nil
}
