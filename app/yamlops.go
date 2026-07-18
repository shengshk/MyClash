package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Category string

const (
	CatForceSniff Category = "force_sniff"
	CatSkipSniff  Category = "skip_sniff"
	CatRealIP     Category = "real_ip"
	CatDirect     Category = "direct"
	CatVip        Category = "vip"
	CatProxy      Category = "proxy"
)

var catLabel = map[Category]string{
	CatForceSniff: "强制嗅探",
	CatSkipSniff:  "跳过嗅探",
	CatRealIP:     "强制真实IP",
	CatDirect:     "强制直连规则",
	CatVip:        "强制重要代理规则",
	CatProxy:      "强制普通代理规则",
}

var listCats = []Category{CatForceSniff, CatSkipSniff, CatRealIP}
var ruleCats = []Category{CatDirect, CatVip, CatProxy}

type Hit struct {
	Cat   Category
	Line  string // exact list/rule line content without leading "- "
	Index int    // line number 0-based in file
}

type Store struct {
	Path string
}

func (s *Store) Read() (string, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Store) Scan(items []Item) map[int][]Hit {
	text, err := s.Read()
	if err != nil {
		return nil
	}
	lines := splitLines(text)
	sections := locateSections(lines)

	out := map[int][]Hit{}
	for i, it := range items {
		var hits []Hit
		for _, cat := range append(listCats, ruleCats...) {
			for _, li := range sections[cat] {
				if lineMatches(lines[li], it) {
					hits = append(hits, Hit{
						Cat:   cat,
						Line:  stripListPrefix(lines[li]),
						Index: li,
					})
				}
			}
		}
		out[i] = hits
	}
	return out
}

func lineMatches(rawLine string, it Item) bool {
	line := stripListPrefix(rawLine)
	line = stripInlineComment(line)
	line = strings.Trim(line, `"'`)
	needles := it.MatchNeedles()

	upper := strings.ToUpper(line)
	switch it.Kind {
	case KindPort:
		// SRC-PORT,443 or DST-PORT,443 or plain in list (unlikely)
		re := regexp.MustCompile(`(?i)^(SRC-PORT|DST-PORT),\s*` + regexp.QuoteMeta(strconv.Itoa(it.Port)) + `$`)
		if re.MatchString(line) {
			return true
		}
		return line == strconv.Itoa(it.Port)
	case KindIP:
		for _, n := range needles {
			if strings.Contains(line, n) {
				return true
			}
		}
		return false
	case KindDomain:
		for _, n := range needles {
			if strings.EqualFold(line, n) {
				return true
			}
			// DOMAIN-SUFFIX,google.com / DOMAIN,x / DOMAIN-KEYWORD,x
			if strings.HasPrefix(upper, "DOMAIN") {
				parts := strings.SplitN(line, ",", 2)
				if len(parts) == 2 && domainEqual(parts[1], n) {
					return true
				}
			}
		}
		return false
	}
	return false
}

func domainEqual(a, b string) bool {
	a = strings.TrimSpace(strings.Trim(a, `"'`))
	b = strings.TrimSpace(strings.Trim(b, `"'`))
	a = strings.TrimPrefix(a, "+.")
	b = strings.TrimPrefix(b, "+.")
	return strings.EqualFold(a, b)
}

func stripListPrefix(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "- ") {
		return strings.TrimSpace(s[2:])
	}
	if s == "-" {
		return ""
	}
	return s
}

func stripInlineComment(s string) string {
	inQuote := false
	var q rune
	for i, r := range s {
		if r == '"' || r == '\'' {
			if !inQuote {
				inQuote = true
				q = r
			} else if r == q {
				inQuote = false
			}
			continue
		}
		if r == '#' && !inQuote {
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}

// locateSections returns line indices of list items under each managed section.
func locateSections(lines []string) map[Category][]int {
	out := map[Category][]int{}
	keys := []struct {
		cat Category
		key string
	}{
		{CatForceSniff, "force-domain:"},
		{CatSkipSniff, "skip-domain:"},
		{CatRealIP, "fake-ip-filter:"},
		{CatDirect, "rule_classical_direct:"},
		{CatVip, "rule_classical_vip:"},
		{CatProxy, "rule_classical_proxy:"},
	}

	var active Category
	var itemIndent = -1
	var inPayload bool
	var payloadIndent = -1
	var sectionIndent = -1

	flush := func() {
		active = ""
		itemIndent = -1
		inPayload = false
		payloadIndent = -1
		sectionIndent = -1
	}

	for i, line := range lines {
		if strings.TrimSpace(line) == "" || isFullComment(line) {
			continue
		}
		indent := leadingSpaces(line)
		trim := strings.TrimSpace(line)

		// leaving current block
		if active != "" && sectionIndent >= 0 && indent <= sectionIndent && !strings.HasPrefix(trim, "#") {
			flush()
		}

		matchedKey := false
		for _, k := range keys {
			if trim == k.key || strings.HasPrefix(trim, k.key+" ") || strings.HasPrefix(trim, k.key+"#") {
				flush()
				active = k.cat
				sectionIndent = indent
				matchedKey = true
				// list cats: items directly under key
				if isListCat(k.cat) {
					itemIndent = indent + 2
					inPayload = true
					payloadIndent = itemIndent
				}
				break
			}
		}
		if matchedKey {
			continue
		}

		if active == "" {
			continue
		}

		// rule cats: wait for payload:
		if isRuleCat(active) {
			if trim == "payload:" || strings.HasPrefix(trim, "payload:") {
				inPayload = true
				payloadIndent = indent + 2
				itemIndent = payloadIndent
				continue
			}
			if !inPayload {
				continue
			}
			if indent < payloadIndent && !strings.HasPrefix(trim, "-") {
				// left payload
				flush()
				continue
			}
		}

		if strings.HasPrefix(trim, "-") && indent >= itemIndent {
			out[active] = append(out[active], i)
		}
	}
	return out
}

func isListCat(c Category) bool {
	for _, x := range listCats {
		if x == c {
			return true
		}
	}
	return false
}

func isRuleCat(c Category) bool {
	for _, x := range ruleCats {
		if x == c {
			return true
		}
	}
	return false
}

func isFullComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
		} else if r == '\t' {
			n += 2
		} else {
			break
		}
	}
	return n
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// Add inserts a new entry into category. For rule cats, ruleLine is full payload value.
func (s *Store) Add(cat Category, entry string) error {
	text, err := s.Read()
	if err != nil {
		return err
	}
	lines := splitLines(text)
	sections := locateSections(lines)
	entry = strings.TrimSpace(entry)

	// duplicate check
	for _, li := range sections[cat] {
		if stripInlineComment(stripListPrefix(lines[li])) == stripInlineComment(entry) {
			return fmt.Errorf("已存在于 %s", catLabel[cat])
		}
	}

	insertAt, indent := insertionPoint(lines, cat, sections[cat])
	pad := strings.Repeat(" ", indent)
	newLine := pad + "- " + entry

	lines = insertLine(lines, insertAt, newLine)
	return s.writeWithBackup(joinLines(lines))
}

func insertionPoint(lines []string, cat Category, existing []int) (at, indent int) {
	if len(existing) > 0 {
		last := existing[len(existing)-1]
		return last + 1, leadingSpaces(lines[last])
	}
	// find key line and insert after it (or after payload:)
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		indentKey := leadingSpaces(line)
		switch cat {
		case CatForceSniff:
			if trim == "force-domain:" || strings.HasPrefix(trim, "force-domain:") {
				return i + 1, indentKey + 2
			}
		case CatSkipSniff:
			if trim == "skip-domain:" || strings.HasPrefix(trim, "skip-domain:") {
				return i + 1, indentKey + 2
			}
		case CatRealIP:
			if trim == "fake-ip-filter:" || strings.HasPrefix(trim, "fake-ip-filter:") {
				return i + 1, indentKey + 2
			}
		case CatDirect, CatVip, CatProxy:
			name := map[Category]string{
				CatDirect: "rule_classical_direct:",
				CatVip:    "rule_classical_vip:",
				CatProxy:  "rule_classical_proxy:",
			}[cat]
			if trim == name || strings.HasPrefix(trim, name) {
				// find payload under this section
				for j := i + 1; j < len(lines); j++ {
					t := strings.TrimSpace(lines[j])
					ind := leadingSpaces(lines[j])
					if ind <= indentKey && t != "" && !strings.HasPrefix(t, "#") {
						break
					}
					if t == "payload:" || strings.HasPrefix(t, "payload:") {
						return j + 1, ind + 2
					}
				}
			}
		}
	}
	return len(lines), 2
}

func insertLine(lines []string, at int, line string) []string {
	if at < 0 {
		at = 0
	}
	if at > len(lines) {
		at = len(lines)
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, line)
	out = append(out, lines[at:]...)
	return out
}

func (s *Store) DeleteLine(index int) error {
	text, err := s.Read()
	if err != nil {
		return err
	}
	lines := splitLines(text)
	if index < 0 || index >= len(lines) {
		return fmt.Errorf("line out of range")
	}
	lines = append(lines[:index], lines[index+1:]...)
	return s.writeWithBackup(joinLines(lines))
}

func (s *Store) writeWithBackup(content string) error {
	// 写前仅保留 1 份隐藏快照，避免 .bak.* 无限堆积
	if _, err := os.Stat(s.Path); err == nil {
		data, err := os.ReadFile(s.Path)
		if err != nil {
			return err
		}
		dir := filepath.Dir(s.Path)
		base := filepath.Base(s.Path)
		bak := filepath.Join(dir, "."+base+".prewrite")
		if err := atomicWriteFile(bak, data, 0o644); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	return atomicWriteFile(s.Path, []byte(content), 0o644)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true

	// 尽量把目录项也刷到盘（rename 持久化）
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
