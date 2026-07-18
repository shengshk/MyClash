package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDomainOptionsMailGoogle(t *testing.T) {
	it, err := parseOne("mail.google.com")
	if err != nil {
		t.Fatal(err)
	}
	// parseOne 取第一个候选（完整主机）；粒度只问本主机精确/后缀，父域已在解析候选里
	opts := BuildDomainOptions(it)
	if len(opts) != 2 {
		t.Fatalf("opts=%+v", opts)
	}
	for _, o := range opts {
		if o.Host != "mail.google.com" {
			t.Fatalf("should not re-offer parent here: %+v", o)
		}
		if o.Host == "com" {
			t.Fatal("must not suggest bare TLD")
		}
	}
}

func TestBuildDomainOptionsGoogle(t *testing.T) {
	it, _ := parseOne("google.com")
	opts := BuildDomainOptions(it)
	if len(opts) != 2 {
		t.Fatalf("opts=%+v", opts)
	}
	// 二级域在 bot 里会直接默认 suffix，此处选项仍含 exact/suffix
}

func TestDomainLabelCountAutoSuffix(t *testing.T) {
	if domainLabelCount("qqssl.fun") != 2 {
		t.Fatal("apex")
	}
	if domainLabelCount("isyn.qqssl.fun") != 3 {
		t.Fatal("sub")
	}
}

func TestFormatEntryDomainModes(t *testing.T) {
	it, _ := parseOne("mail.google.com")
	e, err := FormatEntry(it, WriteSpec{Cat: CatProxy, DomMode: "exact", DomHost: "mail.google.com"})
	if err != nil || e != "DOMAIN,mail.google.com" {
		t.Fatalf("%s %v", e, err)
	}
	e, err = FormatEntry(it, WriteSpec{Cat: CatProxy, DomMode: "suffix", DomHost: "google.com"})
	if err != nil || e != "DOMAIN-SUFFIX,google.com" {
		t.Fatalf("%s %v", e, err)
	}
	e, err = FormatEntry(it, WriteSpec{Cat: CatRealIP, DomMode: "suffix", DomHost: "google.com"})
	if err != nil || e != `"+.google.com"` {
		t.Fatalf("%s %v", e, err)
	}
}

func TestDomainCovers(t *testing.T) {
	wide := parsedEntry{kind: "domain", mode: "suffix", host: "google.com"}
	narrow := parsedEntry{kind: "domain", mode: "exact", host: "mail.google.com"}
	if !domainCovers(wide, narrow) {
		t.Fatal("suffix google should cover mail.google")
	}
	if domainCovers(narrow, wide) {
		t.Fatal("exact mail should not cover suffix google")
	}
}

func TestAnalyzeConflictsMigrateAndShadow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aio.yaml")
	content := `
proxy-groups: []
rules: []
rule-providers:
  rule_classical_direct:
    type: inline
    behavior: classical
    payload:
      - DOMAIN-SUFFIX,google.com
  rule_classical_vip:
    type: inline
    behavior: classical
    payload:
      - DOMAIN,example.com
  rule_classical_proxy:
    type: inline
    behavior: classical
    payload:
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: path}

	// 同款迁移：直连有 DOMAIN-SUFFIX,google.com，写入代理同款
	rep := s.AnalyzeConflicts("DOMAIN-SUFFIX,google.com", CatProxy)
	if len(rep.Migrate) != 1 || rep.Migrate[0].Cat != CatDirect {
		t.Fatalf("migrate=%+v", rep.Migrate)
	}

	// 阴影：直连宽规则盖住拟写入 VIP 的 mail
	rep2 := s.AnalyzeConflicts("DOMAIN,mail.google.com", CatVip)
	if len(rep2.ShadowedBy) != 1 {
		t.Fatalf("shadowedBy=%+v", rep2)
	}

	if err := s.AddMigrating(CatProxy, "DOMAIN-SUFFIX,google.com", rep.Migrate); err != nil {
		t.Fatal(err)
	}
	text, _ := s.Read()
	if strings.Contains(text, "rule_classical_direct:") && strings.Count(text, "DOMAIN-SUFFIX,google.com") != 1 {
		t.Fatalf("expected single google suffix after migrate:\n%s", text)
	}
	if !strings.Contains(text, "rule_classical_proxy:") {
		t.Fatal("proxy section missing")
	}
}

