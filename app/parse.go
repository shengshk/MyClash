package main

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Kind int

const (
	KindDomain Kind = iota
	KindIP
	KindPort
)

type Item struct {
	Raw   string
	Kind  Kind
	Host  string // domain or ip
	CIDR  string // normalized ip/cidr for rules
	Port  int
	Value string // canonical display / match key
}

func ParseItems(text string) ([]Item, []string) {
	var items []Item
	var errs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		got, err := parseLine(line)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", line, err))
			continue
		}
		for _, it := range got {
			key := fmt.Sprintf("%d|%s", it.Kind, it.Value)
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, it)
		}
	}
	return items, errs
}

var (
	rePortOnly = regexp.MustCompile(`^\d{1,5}$`)
	reDomain   = regexp.MustCompile(`(?i)^(\+\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)
)

func parseLine(s string) ([]Item, error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	if s == "" {
		return nil, fmt.Errorf("空内容")
	}

	// port only
	if rePortOnly.MatchString(s) {
		p, _ := strconv.Atoi(s)
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("port out of range")
		}
		return []Item{{Raw: s, Kind: KindPort, Port: p, Value: strconv.Itoa(p)}}, nil
	}

	// URL / host:port/path → 拆 host(域名或 IP) + 端口 + 父域
	if host, port, ok := extractURLParts(s); ok {
		return expandHostPort(s, host, port)
	}

	// IP / CIDR
	if ip, cidr, ok := normalizeIP(s); ok {
		return []Item{{Raw: s, Kind: KindIP, Host: ip, CIDR: cidr, Value: cidr}}, nil
	}

	// 显式 +.domain
	if strings.HasPrefix(s, "+.") {
		host := strings.TrimPrefix(s, "+.")
		host = strings.ToLower(strings.TrimSpace(host))
		if !reDomain.MatchString(host) && !reDomain.MatchString("+."+host) {
			if !looksLikeDomain(host) {
				return nil, fmt.Errorf("无法识别域名")
			}
		}
		return domainCandidates(s, host), nil
	}

	// 带空格的特殊嗅探名
	if strings.Contains(s, " ") {
		return []Item{{Raw: s, Kind: KindDomain, Host: s, Value: s}}, nil
	}

	// 纯域名
	dom := strings.ToLower(strings.TrimSpace(s))
	dom = strings.TrimPrefix(dom, "+.")
	if reDomain.MatchString(dom) || looksLikeDomain(dom) {
		return domainCandidates(s, dom), nil
	}

	return nil, fmt.Errorf("无法识别（需要域名 / IP(CIDR) / 端口 / URL）")
}

// parseOne 保留给测试：取解析出的第一个候选。
func parseOne(s string) (Item, error) {
	items, err := parseLine(s)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, fmt.Errorf("无法识别")
	}
	return items[0], nil
}

func looksLikeDomain(s string) bool {
	if s == "" || strings.ContainsAny(s, ":/?#[]@ ") {
		return false
	}
	if !strings.Contains(s, ".") {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, lb := range labels {
		if lb == "" || len(lb) > 63 {
			return false
		}
	}
	return reDomain.MatchString(s)
}

// extractURLParts 从 URL 或 host:port[/path] 提取 host 与端口。
func extractURLParts(s string) (host string, port int, ok bool) {
	raw := strings.TrimSpace(s)
	lower := strings.ToLower(raw)

	hasScheme := strings.Contains(lower, "://")
	hasPath := strings.Contains(raw, "/")
	// 无 scheme 时：带 path，或 host:port 形式，才当 URL 类
	if !hasScheme {
		if !hasPath && !looksLikeHostPort(raw) {
			return "", 0, false
		}
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", 0, false
	}

	h := u.Hostname()
	if h == "" {
		return "", 0, false
	}
	p := 0
	if u.Port() != "" {
		p, _ = strconv.Atoi(u.Port())
		if p < 1 || p > 65535 {
			p = 0
		}
	}
	return h, p, true
}

func looksLikeHostPort(s string) bool {
	// example.com:443 或 10.0.0.2:9001（不要把普通域名当 URL）
	if strings.Count(s, ":") != 1 {
		return false
	}
	host, portStr, ok := strings.Cut(s, ":")
	if !ok || host == "" || portStr == "" {
		return false
	}
	if strings.ContainsAny(portStr, "/?#") {
		return false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return looksLikeDomain(host)
}

func expandHostPort(raw, host string, port int) ([]Item, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("URL 无主机名")
	}

	var out []Item
	if ip, cidr, ok := normalizeIP(host); ok {
		out = append(out, Item{Raw: raw, Kind: KindIP, Host: ip, CIDR: cidr, Value: cidr})
	} else {
		h := strings.ToLower(host)
		if !looksLikeDomain(h) {
			return nil, fmt.Errorf("无法识别主机：%s", host)
		}
		out = append(out, domainCandidates(raw, h)...)
	}
	if port > 0 {
		out = append(out, Item{Raw: raw, Kind: KindPort, Port: port, Value: strconv.Itoa(port)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("未能从链接解析出域名/IP/端口")
	}
	return out, nil
}

// domainCandidates：完整主机 + 父域（≥2 标签，不含纯 TLD）。
func domainCandidates(raw, host string) []Item {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "+.")
	labels := strings.Split(host, ".")
	var out []Item
	seen := map[string]bool{}
	add := func(h string) {
		if h == "" || seen[h] {
			return
		}
		if strings.Count(h, ".")+1 < 2 {
			return
		}
		seen[h] = true
		out = append(out, Item{Raw: raw, Kind: KindDomain, Host: h, Value: h})
	}
	add(host)
	for i := 1; i < len(labels); i++ {
		parent := strings.Join(labels[i:], ".")
		add(parent)
	}
	return out
}

func normalizeIP(s string) (ipStr, cidr string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}

	if strings.HasSuffix(s, "/") {
		base := strings.TrimSuffix(s, "/")
		ip := net.ParseIP(base)
		if ip == nil {
			return "", "", false
		}
		if ip.To4() != nil {
			return base, base + "/32", true
		}
		return base, base + "/128", true
	}

	if strings.Contains(s, "/") {
		ip, network, err := net.ParseCIDR(s)
		if err != nil {
			return "", "", false
		}
		ones, _ := network.Mask.Size()
		return ip.String(), fmt.Sprintf("%s/%d", network.IP.String(), ones), true
	}

	ip := net.ParseIP(s)
	if ip == nil {
		return "", "", false
	}
	if ip.To4() != nil {
		return ip.String(), ip.String() + "/32", true
	}
	return ip.String(), ip.String() + "/128", true
}

// MatchNeedle returns strings that should be searched in yaml list values / rule payloads.
func (it Item) MatchNeedles() []string {
	switch it.Kind {
	case KindPort:
		return []string{strconv.Itoa(it.Port)}
	case KindIP:
		out := []string{it.CIDR, it.Host}
		if !strings.Contains(it.Raw, "/") {
			out = append(out, it.Raw)
		}
		return unique(out)
	case KindDomain:
		out := []string{it.Value, it.Host, "+." + it.Host}
		return unique(out)
	}
	return nil
}

func unique(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// domainLabelCount 返回域名标签数；非域名返回 0。
func domainLabelCount(host string) int {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || strings.Contains(host, " ") {
		return 0
	}
	return strings.Count(host, ".") + 1
}
