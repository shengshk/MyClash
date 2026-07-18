package main

import (
	"fmt"
	"sort"
	"strings"
)

// DomainOption 是写入前可选的匹配粒度。
type DomainOption struct {
	Mode  string // exact | suffix
	Host  string
	Label string
	Hint  string // 短说明
	Rec   bool   // 推荐
}

// BuildDomainOptions 根据用户输入生成粒度选项（不含纯 TLD）。
// 用户已写 +.xxx 时返回 nil（调用方应直接采用 suffix）。
func BuildDomainOptions(it Item) []DomainOption {
	if it.Kind != KindDomain {
		return nil
	}
	host := strings.TrimSpace(strings.ToLower(it.Host))
	if host == "" || strings.Contains(host, " ") {
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(it.Raw), "+.") {
		return nil
	}

	labels := strings.Split(host, ".")
	var opts []DomainOption

	// 父域已在解析阶段作为独立候选抛出；此处只问「本主机」精确 vs 后缀。
	opts = append(opts, DomainOption{
		Mode:  "exact",
		Host:  host,
		Label: "仅 " + host,
		Hint:  "精确匹配",
	})
	opts = append(opts, DomainOption{
		Mode:  "suffix",
		Host:  host,
		Label: "+." + host,
		Hint:  "含其所有子域",
		Rec:   len(labels) >= 3,
	})
	return opts
}

type WriteSpec struct {
	Cat     Category
	Dir     string // SRC/DST 或 IP 方向；空=默认
	DomMode string // exact | suffix
	DomHost string
}

func FormatEntry(it Item, spec WriteSpec) (string, error) {
	cat := spec.Cat
	switch {
	case isListCat(cat):
		switch it.Kind {
		case KindDomain:
			if strings.Contains(it.Host, " ") {
				return `"` + it.Host + `"`, nil
			}
			host := spec.DomHost
			if host == "" {
				host = it.Host
			}
			mode := spec.DomMode
			if mode == "" {
				mode = "suffix"
				if strings.HasPrefix(strings.TrimSpace(it.Raw), "+.") {
					mode = "suffix"
				}
			}
			if mode == "exact" {
				return `"` + host + `"`, nil
			}
			return `"` + "+." + host + `"`, nil
		case KindIP:
			return `"` + it.CIDR + `"`, nil
		case KindPort:
			return "", fmt.Errorf("%s 不支持端口", catLabel[cat])
		}
	case isRuleCat(cat):
		switch it.Kind {
		case KindDomain:
			if strings.Contains(it.Host, " ") {
				return "", fmt.Errorf("规则不支持带空格域名")
			}
			host := spec.DomHost
			if host == "" {
				host = it.Host
			}
			mode := spec.DomMode
			if mode == "" {
				mode = "suffix"
			}
			if mode == "exact" {
				return "DOMAIN," + host, nil
			}
			return "DOMAIN-SUFFIX," + host, nil
		case KindIP:
			prefix := "IP-CIDR"
			switch spec.Dir {
			case "SRC":
				prefix = "SRC-IP-CIDR"
			case "DST":
				prefix = "DST-IP-CIDR"
			}
			return prefix + "," + it.CIDR, nil
		case KindPort:
			if spec.Dir != "SRC" && spec.Dir != "DST" {
				return "", fmt.Errorf("端口规则需要选择 SRC 或 DST")
			}
			return spec.Dir + "-PORT," + fmt.Sprintf("%d", it.Port), nil
		}
	}
	return "", fmt.Errorf("unsupported")
}

// 规则组优先级：数字越小越先命中（与 aio.yaml RULE-SET 顺序一致）
func rulePriority(c Category) int {
	switch c {
	case CatDirect:
		return 0
	case CatVip:
		return 1
	case CatProxy:
		return 2
	default:
		return 100
	}
}

func exclusivePeerCats(target Category) []Category {
	switch target {
	case CatDirect:
		return []Category{CatVip, CatProxy}
	case CatVip:
		return []Category{CatDirect, CatProxy}
	case CatProxy:
		return []Category{CatDirect, CatVip}
	case CatForceSniff:
		return []Category{CatSkipSniff}
	case CatSkipSniff:
		return []Category{CatForceSniff}
	default:
		return nil
	}
}

type parsedEntry struct {
	raw    string
	kind   string // domain | ip | port | other
	mode   string // exact | suffix (domain)
	host   string // domain host / ip cidr / port num
	prefix string // DOMAIN / DOMAIN-SUFFIX / IP-CIDR / SRC-PORT ...
}

func parseStoredEntry(line string, cat Category) parsedEntry {
	raw := stripInlineComment(stripListPrefix(line))
	raw = strings.TrimSpace(raw)
	p := parsedEntry{raw: raw}
	upper := strings.ToUpper(raw)

	if isRuleCat(cat) || strings.Contains(upper, ",") {
		parts := strings.SplitN(raw, ",", 2)
		if len(parts) != 2 {
			p.kind = "other"
			return p
		}
		p.prefix = strings.ToUpper(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(strings.Trim(parts[1], `"'`))
		switch p.prefix {
		case "DOMAIN":
			p.kind, p.mode, p.host = "domain", "exact", strings.ToLower(val)
		case "DOMAIN-SUFFIX":
			p.kind, p.mode, p.host = "domain", "suffix", strings.ToLower(val)
		case "DOMAIN-KEYWORD":
			p.kind, p.host = "other", strings.ToLower(val)
		case "IP-CIDR", "IP-CIDR6", "SRC-IP-CIDR", "DST-IP-CIDR":
			p.kind, p.host = "ip", val
		case "SRC-PORT", "DST-PORT":
			p.kind, p.host = "port", val
		default:
			p.kind = "other"
			p.host = val
		}
		return p
	}

	// list 项：嗅探 / fake-ip
	v := strings.Trim(raw, `"'`)
	if strings.HasPrefix(v, "+.") {
		p.kind, p.mode, p.host = "domain", "suffix", strings.ToLower(strings.TrimPrefix(v, "+."))
		return p
	}
	if strings.Contains(v, "/") || netLooksIP(v) {
		p.kind, p.host = "ip", v
		return p
	}
	p.kind, p.mode, p.host = "domain", "exact", strings.ToLower(v)
	return p
}

func netLooksIP(s string) bool {
	// 粗判，避免引入循环依赖细节
	if strings.Count(s, ":") >= 2 {
		return true
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func parseNewEntry(entry string, cat Category) parsedEntry {
	return parseStoredEntry("- "+entry, cat)
}

func entriesExactSame(a, b parsedEntry) bool {
	if a.kind == "other" || b.kind == "other" {
		return strings.EqualFold(strings.Trim(a.raw, `"'`), strings.Trim(b.raw, `"'`))
	}
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case "domain":
		return a.mode == b.mode && a.host == b.host
	case "ip", "port":
		return a.host == b.host && a.prefix == b.prefix
	}
	return false
}

// domainCovers：宽规则 A 是否盖住窄规则 B（同 Clash 后缀语义）
func domainCovers(wide, narrow parsedEntry) bool {
	if wide.kind != "domain" || narrow.kind != "domain" {
		return false
	}
	if wide.mode != "suffix" {
		// 精确只盖住完全相同 host 的精确/后缀自身
		return wide.mode == "exact" && wide.host == narrow.host && narrow.mode == "exact"
	}
	if narrow.host == wide.host {
		return true
	}
	return strings.HasSuffix(narrow.host, "."+wide.host)
}

type ConflictReport struct {
	SameCat    *Hit  // 目标类已存在同款 → 禁止再写
	Migrate    []Hit // 互斥类同款 → 写入时自动删除
	ShadowedBy []Hit // 更靠前的宽规则会盖住本次写入
	Shadows    []Hit // 本次写入会盖住更靠后组里的已有项
}

func (s *Store) AnalyzeConflicts(entry string, cat Category) ConflictReport {
	var rep ConflictReport
	text, err := s.Read()
	if err != nil {
		return rep
	}
	lines := splitLines(text)
	sections := locateSections(lines)
	neu := parseNewEntry(entry, cat)

	checkCats := append([]Category{cat}, exclusivePeerCats(cat)...)
	seen := map[string]bool{}

	for _, c := range checkCats {
		for _, li := range sections[c] {
			raw := stripInlineComment(stripListPrefix(lines[li]))
			old := parseStoredEntry(lines[li], c)
			key := fmt.Sprintf("%s:%d", c, li)
			if seen[key] {
				continue
			}
			h := Hit{Cat: c, Line: raw, Index: li}

			if entriesExactSame(neu, old) {
				seen[key] = true
				if c == cat {
					cp := h
					rep.SameCat = &cp
				} else {
					rep.Migrate = append(rep.Migrate, h)
				}
				continue
			}

			// 父子域阴影（仅规则组 / 嗅探组）
			if neu.kind != "domain" || old.kind != "domain" {
				continue
			}
			if isRuleCat(cat) && isRuleCat(c) {
				if rulePriority(c) < rulePriority(cat) && domainCovers(old, neu) {
					seen[key] = true
					rep.ShadowedBy = append(rep.ShadowedBy, h)
				}
				if rulePriority(c) > rulePriority(cat) && domainCovers(neu, old) {
					seen[key] = true
					rep.Shadows = append(rep.Shadows, h)
				}
			}
			// 嗅探强制/跳过：无顺序盖住，同款已处理；父子仅提示 shadow 对称
			if (cat == CatForceSniff || cat == CatSkipSniff) && (c == CatForceSniff || c == CatSkipSniff) && c != cat {
				if domainCovers(old, neu) || domainCovers(neu, old) {
					seen[key] = true
					rep.ShadowedBy = append(rep.ShadowedBy, h) // 当作需注意
				}
			}
		}
	}
	return rep
}

// AddMigrating 写入新项，并在同一次原子写入中删除互斥类同款。
func (s *Store) AddMigrating(cat Category, entry string, migrate []Hit) error {
	text, err := s.Read()
	if err != nil {
		return err
	}
	lines := splitLines(text)
	sections := locateSections(lines)
	entry = strings.TrimSpace(entry)

	for _, li := range sections[cat] {
		if entriesExactSame(parseNewEntry(entry, cat), parseStoredEntry(lines[li], cat)) {
			return fmt.Errorf("已存在于 %s", catLabel[cat])
		}
	}

	// 从后往前删，避免行号错位
	idxs := make([]int, 0, len(migrate))
	for _, h := range migrate {
		idxs = append(idxs, h.Index)
	}
	sort.Slice(idxs, func(i, j int) bool { return idxs[i] > idxs[j] })
	for _, i := range idxs {
		if i < 0 || i >= len(lines) {
			continue
		}
		lines = append(lines[:i], lines[i+1:]...)
	}

	// 删除后重新定位插入点
	sections = locateSections(lines)
	insertAt, indent := insertionPoint(lines, cat, sections[cat])
	pad := strings.Repeat(" ", indent)
	lines = insertLine(lines, insertAt, pad+"- "+entry)
	return s.writeWithBackup(joinLines(lines))
}
