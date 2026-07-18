package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Source struct {
	Slot int
	Name string
	URL  string
	Path string
}

func (a Source) Alias() string  { return fmt.Sprintf("*p%d", a.Slot) }
func (a Source) AliasN() string { return fmt.Sprintf("*p%dn", a.Slot) }
func (a Source) NoHK() string   { return a.Name + "-NoHK" }

var (
	reRegP  = regexp.MustCompile(`(?m)^x-p(\d+):\s*&p\d+\s+(\S+)\s*$`)
	reRegPN = regexp.MustCompile(`(?m)^x-p(\d+)n:\s*&p\d+n\s+(\S+)\s*$`)
	rePreferLine = regexp.MustCompile(`(?m)^(\s*-\s*\{name:\s*♻️ 永不失联,.+proxies:\s*)\[([^\]]*)\](.*)$`)
	reManualLine = regexp.MustCompile(`(?m)^(\s*-\s*\{name:\s*🖐 手动选择,.+proxies:\s*)\[([^\]]*)\](.*)$`)
	reProxyAnchor = regexp.MustCompile(`(?m)^(x-proxy:\s*&proxy\s+)\[([^\]]*)\]\s*$`)
	reProxyNoHKAnchor = regexp.MustCompile(`(?m)^(x-proxy-NoHK:\s*&proxy-NoHK\s+)\[([^\]]*)\]\s*$`)
	reGroupLineP  = regexp.MustCompile(`(?m)^\s*-\s*\{name:\s*\*p(\d+),.+\}\s*$`)
	reGroupLinePN = regexp.MustCompile(`(?m)^\s*-\s*\{name:\s*\*p(\d+)n,.+\}\s*$`)
	reSourceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
)

func ValidateSourceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if !reSourceName.MatchString(name) {
		return fmt.Errorf("名称仅允许字母数字和 _ -，且不能以符号开头")
	}
	if strings.HasSuffix(strings.ToLower(name), "-nohk") {
		return fmt.Errorf("名称不要带 -NoHK 后缀（会自动生成）")
	}
	return nil
}

func ValidateSourceURL(raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("URL 不合法，需要 http(s)://…")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL 仅支持 http/https")
	}
	return nil
}

func (s *Store) ListSources() ([]Source, error) {
	text, err := s.Read()
	if err != nil {
		return nil, err
	}
	return parseSources(text), nil
}

func parseSources(text string) []Source {
	bySlot := map[int]*Source{}
	for _, m := range reRegP.FindAllStringSubmatch(text, -1) {
		slot, _ := strconv.Atoi(m[1])
		a := bySlot[slot]
		if a == nil {
			a = &Source{Slot: slot}
			bySlot[slot] = a
		}
		a.Name = m[2]
	}
	// enrich URL/path from providers
	for slot, a := range bySlot {
		if a.Name == "" {
			delete(bySlot, slot)
			continue
		}
		a.URL, a.Path = providerMeta(text, a.Name)
	}
	out := make([]Source, 0, len(bySlot))
	for _, a := range bySlot {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

func providerMeta(text, name string) (urlStr, pathStr string) {
	// Match provider block key then url/path lines
	reBlock := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `:\s*$`)
	loc := reBlock.FindStringIndex(text)
	if loc == nil {
		return "", ""
	}
	rest := text[loc[1]:]
	// until next top-level provider key (2 spaces + name + :) or section at col 0
	end := len(rest)
	for i, line := range strings.Split(rest, "\n") {
		if i == 0 {
			continue
		}
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			// compute byte offset
			end = 0
			parts := strings.SplitN(rest, "\n", i+1)
			for j := 0; j < i; j++ {
				end += len(parts[j]) + 1
			}
			break
		}
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			continue
		}
		// next provider at indent 2: "  FOO:"
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(trim, ":") && !strings.Contains(trim, " ") {
			end = 0
			parts := strings.SplitN(rest, "\n", i+1)
			for j := 0; j < i; j++ {
				end += len(parts[j]) + 1
			}
			break
		}
	}
	block := rest
	if end < len(rest) {
		block = rest[:end]
	}
	if m := regexp.MustCompile(`(?m)^\s+url:\s*(\S+)\s*$`).FindStringSubmatch(block); len(m) == 2 {
		urlStr = m[1]
	}
	if m := regexp.MustCompile(`(?m)^\s+path:\s*(\S+)\s*$`).FindStringSubmatch(block); len(m) == 2 {
		pathStr = m[1]
	}
	return urlStr, pathStr
}

func (s *Store) PreferredSource() (Source, []Source, error) {
	text, err := s.Read()
	if err != nil {
		return Source{}, nil, err
	}
	return PreferredFromText(text)
}

func (s *Store) UpdateSourceURL(name, newURL string) error {
	if err := ValidateSourceURL(newURL); err != nil {
		return err
	}
	text, err := s.Read()
	if err != nil {
		return err
	}
	all := parseSources(text)
	found := false
	for _, a := range all {
		if a.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("订阅不存在：%s", name)
	}
	lines := splitLines(text)
	header := "  " + name + ":"
	inBlock := false
	replaced := false
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == header {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		ind := leadingSpaces(line)
		trim := strings.TrimSpace(line)
		if ind < 2 || (ind == 2 && strings.HasSuffix(trim, ":") && !strings.HasPrefix(trim, "url:")) {
			break
		}
		if strings.HasPrefix(trim, "url:") {
			pad := strings.Repeat(" ", ind)
			lines[i] = pad + "url: " + strings.TrimSpace(newURL)
			replaced = true
			break
		}
	}
	if !replaced {
		return fmt.Errorf("找不到订阅 url 行：%s", name)
	}
	return s.writeWithBackup(joinLines(lines))
}

func preferAliasOrder(text string) []string {
	m := rePreferLine.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	return parseAliasList(m[2])
}

func parseAliasList(inner string) []string {
	var out []string
	for _, p := range strings.Split(inner, ",") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "*p") && !strings.HasSuffix(p, "n") {
			out = append(out, p)
		}
	}
	return out
}

func lowestFreeSlot(sources []Source) int {
	used := map[int]bool{}
	for _, a := range sources {
		used[a.Slot] = true
	}
	for i := 1; ; i++ {
		if !used[i] {
			return i
		}
	}
}

func (s *Store) SetPreferred(name string) error {
	text, err := s.Read()
	if err != nil {
		return err
	}
	all := parseSources(text)
	var target *Source
	for i := range all {
		if strings.EqualFold(all[i].Name, name) {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("订阅不存在：%s", name)
	}
	order := preferAliasOrder(text)
	if len(order) == 0 {
		for _, a := range all {
			order = append(order, a.Alias())
		}
	}
	newOrder := []string{target.Alias()}
	for _, al := range order {
		if al == target.Alias() {
			continue
		}
		newOrder = append(newOrder, al)
	}
	text = rePreferLine.ReplaceAllString(text, "${1}["+strings.Join(newOrder, ", ")+"]${3}")
	return s.writeWithBackup(text)
}

func (s *Store) RenameSource(oldName, newName string) error {
	if err := ValidateSourceName(newName); err != nil {
		return err
	}
	newName = strings.TrimSpace(newName)
	text, err := s.Read()
	if err != nil {
		return err
	}
	all := parseSources(text)
	var slot int
	found := false
	for _, a := range all {
		if a.Name == oldName {
			slot = a.Slot
			found = true
		}
		if strings.EqualFold(a.Name, newName) && a.Name != oldName {
			return fmt.Errorf("名称已存在：%s", newName)
		}
	}
	if !found {
		return fmt.Errorf("订阅不存在：%s", oldName)
	}
	oldNoHK := oldName + "-NoHK"
	newNoHK := newName + "-NoHK"

	// registry values
	text = regexp.MustCompile(`(?m)^(x-p`+strconv.Itoa(slot)+`:\s*&p`+strconv.Itoa(slot)+`\s+)\S+\s*$`).
		ReplaceAllString(text, "${1}"+newName)
	text = regexp.MustCompile(`(?m)^(x-p`+strconv.Itoa(slot)+`n:\s*&p`+strconv.Itoa(slot)+`n\s+)\S+\s*$`).
		ReplaceAllString(text, "${1}"+newNoHK)

	// provider key + path + any leftover name refs in that block header
	text = regexp.MustCompile(`(?m)^  `+regexp.QuoteMeta(oldName)+`:\s*$`).
		ReplaceAllString(text, "  "+newName+":")
	text = strings.ReplaceAll(text, "./providers/"+oldName+".yaml", "./providers/"+newName+".yaml")
	// safety: old NoHK string shouldn't appear as group name via alias; aliases unchanged

	if err := s.writeWithBackup(text); err != nil {
		return err
	}
	_ = oldNoHK
	return renameProviderFile(s.Path, oldName, newName)
}

func renameProviderFile(yamlPath, oldName, newName string) error {
	dir := filepath.Join(filepath.Dir(yamlPath), "providers")
	oldP := filepath.Join(dir, oldName+".yaml")
	newP := filepath.Join(dir, newName+".yaml")
	if _, err := os.Stat(oldP); err != nil {
		return nil // no cache yet
	}
	if _, err := os.Stat(newP); err == nil {
		_ = os.Remove(oldP)
		return nil
	}
	return os.Rename(oldP, newP)
}

func (s *Store) DeleteSource(name string) (wasPreferred bool, nextPrefer string, err error) {
	text, err := s.Read()
	if err != nil {
		return false, "", err
	}
	all := parseSources(text)
	var target *Source
	for i := range all {
		if all[i].Name == name {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return false, "", fmt.Errorf("订阅不存在：%s", name)
	}
	pref, _, _ := PreferredFromText(text)
	wasPreferred = pref.Name == name

	slot := target.Slot
	alias, aliasN := target.Alias(), target.AliasN()

	// remove registry lines
	text = regexp.MustCompile(`(?m)^x-p`+strconv.Itoa(slot)+`:\s*&p`+strconv.Itoa(slot)+`\s+\S+\s*\n`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`(?m)^x-p`+strconv.Itoa(slot)+`n:\s*&p`+strconv.Itoa(slot)+`n\s+\S+\s*\n`).ReplaceAllString(text, "")

	// remove provider block
	text = removeProviderBlock(text, name)

	// remove group lines
	text = regexp.MustCompile(`(?m)^\s*-\s*\{name:\s*\*p`+strconv.Itoa(slot)+`n,.+\}\s*\n`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`(?m)^\s*-\s*\{name:\s*\*p`+strconv.Itoa(slot)+`,.+\}\s*\n`).ReplaceAllString(text, "")

	// strip aliases from lists
	text = stripAliasEverywhere(text, alias)
	text = stripAliasEverywhere(text, aliasN)

	remaining := parseSources(text)
	if wasPreferred && len(remaining) > 0 {
		order := preferAliasOrder(text)
		byAlias := map[string]string{}
		for _, a := range remaining {
			byAlias[a.Alias()] = a.Name
		}
		for _, al := range order {
			if n, ok := byAlias[al]; ok {
				nextPrefer = n
				break
			}
		}
		if nextPrefer == "" {
			nextPrefer = remaining[0].Name
		}
	}

	if err := s.writeWithBackup(text); err != nil {
		return false, "", err
	}
	_ = os.Remove(filepath.Join(filepath.Dir(s.Path), "providers", name+".yaml"))
	return wasPreferred, nextPrefer, nil
}

func PreferredFromText(text string) (Source, []Source, error) {
	all := parseSources(text)
	order := preferAliasOrder(text)
	if len(all) == 0 {
		return Source{}, all, fmt.Errorf("没有订阅")
	}
	if len(order) == 0 {
		return all[0], all, nil
	}
	byAlias := map[string]Source{}
	for _, a := range all {
		byAlias[a.Alias()] = a
	}
	for _, al := range order {
		if a, ok := byAlias[al]; ok {
			return a, all, nil
		}
	}
	return all[0], all, nil
}

func removeProviderBlock(text, name string) string {
	lines := splitLines(text)
	out := make([]string, 0, len(lines))
	skip := false
	header := "  " + name + ":"
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if !skip && (line == header || strings.TrimRight(line, " \t") == header) {
			skip = true
			continue
		}
		if skip {
			if line == "" {
				// peek: empty inside block ok; if next is dedented section keep skipping empties until content
				continue
			}
			ind := leadingSpaces(line)
			if strings.HasPrefix(trim, "#") && ind >= 2 {
				continue
			}
			// still in block: indented more than 2, or exactly "    ..."
			if ind >= 4 || (ind == 2 && strings.HasPrefix(trim, "<<:")) {
				continue
			}
			if ind == 2 && strings.HasSuffix(trim, ":") && !strings.Contains(trim, " ") {
				// next provider
				skip = false
				out = append(out, line)
				continue
			}
			if ind < 2 {
				skip = false
				out = append(out, line)
				continue
			}
			continue
		}
		out = append(out, line)
	}
	return joinLines(out)
}

func stripAliasEverywhere(text, alias string) string {
	// rewrite any [...] that contains the alias
	reList := regexp.MustCompile(`\[([^\[\]]*)\]`)
	return reList.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[1 : len(m)-1]
		parts := strings.Split(inner, ",")
		var keep []string
		changed := false
		for _, p := range parts {
			t := strings.TrimSpace(p)
			if t == alias {
				changed = true
				continue
			}
			keep = append(keep, strings.TrimSpace(p))
		}
		if !changed {
			return m
		}
		// normalize spacing
		for i := range keep {
			keep[i] = strings.TrimSpace(keep[i])
		}
		return "[" + strings.Join(keep, ", ") + "]"
	})
}

func (s *Store) AddSource(name, rawURL string) (Source, error) {
	if err := ValidateSourceName(name); err != nil {
		return Source{}, err
	}
	if err := ValidateSourceURL(rawURL); err != nil {
		return Source{}, err
	}
	name = strings.TrimSpace(name)
	rawURL = strings.TrimSpace(rawURL)

	text, err := s.Read()
	if err != nil {
		return Source{}, err
	}
	all := parseSources(text)
	for _, a := range all {
		if strings.EqualFold(a.Name, name) {
			return Source{}, fmt.Errorf("名称已存在：%s", name)
		}
	}
	slot := lowestFreeSlot(all)
	src := Source{Slot: slot, Name: name, URL: rawURL, Path: "./providers/" + name + ".yaml"}

	text = insertRegistry(text, src)
	text = insertProvider(text, src)
	text = insertGroupLines(text, src)
	text = addAliasesToLists(text, src)

	if err := s.writeWithBackup(text); err != nil {
		return Source{}, err
	}
	return src, nil
}

func insertRegistry(text string, src Source) string {
	lineP := fmt.Sprintf("x-p%d:  &p%d  %s", src.Slot, src.Slot, src.Name)
	linePN := fmt.Sprintf("x-p%dn: &p%dn %s", src.Slot, src.Slot, src.NoHK())
	// insert in numeric order among x-p* lines
	lines := splitLines(text)
	insertAt := -1
	lastReg := -1
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if reRegP.MatchString(trim) || reRegPN.MatchString(trim) ||
			regexp.MustCompile(`^x-p\d+n?:`).MatchString(trim) {
			lastReg = i
			// if this slot number > src.Slot, insert before its p line
			if m := regexp.MustCompile(`^x-p(\d+)n?:`).FindStringSubmatch(trim); m != nil {
				n, _ := strconv.Atoi(m[1])
				if n > src.Slot && insertAt < 0 {
					insertAt = i
				}
			}
		}
	}
	if insertAt < 0 {
		insertAt = lastReg + 1
	}
	block := []string{lineP, linePN}
	out := append([]string{}, lines[:insertAt]...)
	out = append(out, block...)
	out = append(out, lines[insertAt:]...)
	return joinLines(out)
}

func insertProvider(text string, src Source) string {
	block := fmt.Sprintf("  %s:\n    <<: *provider\n    url: %s\n    path: %s", src.Name, src.URL, src.Path)
	lines := splitLines(text)
	// find proxy-providers: then insert before next top-level key after last provider
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "proxy-providers:" {
			start = i
			break
		}
	}
	if start < 0 {
		return text + "\n" + block + "\n"
	}
	insertAt := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trim := strings.TrimSpace(lines[i])
		ind := leadingSpaces(lines[i])
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if ind == 0 {
			insertAt = i
			break
		}
	}
	out := append([]string{}, lines[:insertAt]...)
	out = append(out, block)
	out = append(out, lines[insertAt:]...)
	return joinLines(out)
}

func insertGroupLines(text string, src Source) string {
	lineN := fmt.Sprintf("  - {name: *p%dn, <<: *url-test, use: [*p%d], filter: *no-hongkong}", src.Slot, src.Slot)
	lineP := fmt.Sprintf("  - {name: *p%d, <<: *url-test, use: [*p%d]}", src.Slot, src.Slot)
	lines := splitLines(text)

	// insert NoHK among NoHK lines, full among full lines
	insertNAt, insertPAt := -1, -1
	lastN, lastP := -1, -1
	section2 := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.Contains(trim, "订阅基础分组") || strings.Contains(trim, "机场基础分组") {
			section2 = true
		}
		if !section2 {
			continue
		}
		if m := reGroupLinePN.FindStringSubmatch(line); m != nil {
			lastN = i
			n, _ := strconv.Atoi(m[1])
			if n > src.Slot && insertNAt < 0 {
				insertNAt = i
			}
		}
		if m := reGroupLineP.FindStringSubmatch(line); m != nil {
			lastP = i
			n, _ := strconv.Atoi(m[1])
			if n > src.Slot && insertPAt < 0 {
				insertPAt = i
			}
		}
	}
	if insertNAt < 0 {
		insertNAt = lastN + 1
	}
	if insertPAt < 0 {
		insertPAt = lastP + 1
	}
	if insertNAt <= 0 && lastN < 0 {
		// fallback: before 地区专项
		for i, line := range lines {
			if strings.Contains(line, "地区专项") {
				insertNAt, insertPAt = i, i
				break
			}
		}
	}

	// insert higher index first
	type ins struct {
		at   int
		line string
	}
	ops := []ins{{insertNAt, lineN}, {insertPAt, lineP}}
	if ops[0].at > ops[1].at {
		ops[0], ops[1] = ops[1], ops[0]
	}
	// after first insert, second at may shift
	out := lines
	for i := len(ops) - 1; i >= 0; i-- {
		at := ops[i].at
		if at < 0 {
			at = len(out)
		}
		// recompute if needed — use simple append approach differently
		_ = at
	}

	// redo carefully: insert NoHK first, then full (adjusting index)
	nAt := insertNAt
	if nAt < 0 {
		nAt = len(lines)
	}
	out = insertLine(lines, nAt, lineN)
	pAt := insertPAt
	if insertPAt >= nAt {
		pAt = insertPAt + 1
	}
	if pAt < 0 {
		pAt = len(out)
	}
	out = insertLine(out, pAt, lineP)
	return joinLines(out)
}

func addAliasesToLists(text string, src Source) string {
	// x-proxy: insert *pN before DIRECT
	text = reProxyAnchor.ReplaceAllStringFunc(text, func(line string) string {
		m := reProxyAnchor.FindStringSubmatch(line)
		inner := injectBeforeDirect(m[2], src.Alias())
		return m[1] + "[" + inner + "]"
	})
	text = reProxyNoHKAnchor.ReplaceAllStringFunc(text, func(line string) string {
		m := reProxyNoHKAnchor.FindStringSubmatch(line)
		inner := injectBeforeDirect(m[2], src.AliasN())
		return m[1] + "[" + inner + "]"
	})
	text = rePreferLine.ReplaceAllStringFunc(text, func(line string) string {
		m := rePreferLine.FindStringSubmatch(line)
		inner := appendAlias(m[2], src.Alias())
		return m[1] + "[" + inner + "]" + m[3]
	})
	text = reManualLine.ReplaceAllStringFunc(text, func(line string) string {
		m := reManualLine.FindStringSubmatch(line)
		inner := injectManual(m[2], src)
		return m[1] + "[" + inner + "]" + m[3]
	})
	return text
}

func injectBeforeDirect(inner, alias string) string {
	parts := splitList(inner)
	for _, p := range parts {
		if p == alias {
			return joinList(parts)
		}
	}
	out := make([]string, 0, len(parts)+1)
	inserted := false
	for _, p := range parts {
		if !inserted && (p == "DIRECT" || strings.Contains(p, "永不失联") || strings.Contains(p, "手动选择")) {
			out = append(out, alias)
			inserted = true
		}
		out = append(out, p)
	}
	if !inserted {
		out = append(out, alias)
	}
	return joinList(out)
}

func appendAlias(inner, alias string) string {
	parts := splitList(inner)
	for _, p := range parts {
		if p == alias {
			return joinList(parts)
		}
	}
	parts = append(parts, alias)
	return joinList(parts)
}

func injectManual(inner string, src Source) string {
	parts := splitList(inner)
	hasN, hasP := false, false
	for _, p := range parts {
		if p == src.AliasN() {
			hasN = true
		}
		if p == src.Alias() {
			hasP = true
		}
	}
	var out []string
	insertedN, insertedP := hasN, hasP
	// NoHK block then full block — insert before first *pN (non-n) for NoHK? 
	// Structure: all *pXn then all *pX
	seenFull := false
	for _, p := range parts {
		if strings.HasPrefix(p, "*p") && !strings.HasSuffix(p, "n") {
			if !seenFull {
				seenFull = true
				if !insertedN {
					out = append(out, src.AliasN())
					insertedN = true
				}
			}
			if !insertedP {
				// insert full alias in slot order among fulls — append before end of fulls later
			}
		}
		out = append(out, p)
	}
	if !insertedN {
		// prepend-ish: before first full
		tmp := out
		out = nil
		done := false
		for _, p := range tmp {
			if !done && strings.HasPrefix(p, "*p") && !strings.HasSuffix(p, "n") {
				out = append(out, src.AliasN())
				done = true
			}
			out = append(out, p)
		}
		if !done {
			out = append(out, src.AliasN())
		}
		insertedN = true
	}
	if !insertedP {
		out = append(out, src.Alias())
	}
	// dedupe
	seen := map[string]bool{}
	var uniq []string
	for _, p := range out {
		if seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}
	return joinList(uniq)
}

func splitList(inner string) []string {
	var out []string
	for _, p := range strings.Split(inner, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinList(parts []string) string {
	return strings.Join(parts, ", ")
}
