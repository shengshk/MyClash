package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeIP(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"1.1.1.1", "1.1.1.1/32", true},
		{"1.1.1.1/", "1.1.1.1/32", true},
		{"1.1.1.1/24", "1.1.1.0/24", true},
		{"google.com", "", false},
	}
	for _, c := range cases {
		_, got, ok := normalizeIP(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("%s => %s,%v want %s,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseItemsMultiline(t *testing.T) {
	items, errs := ParseItems("google.com\n1.1.1.1/\n443\nbad!!")
	if len(errs) != 1 {
		t.Fatalf("errs=%v", errs)
	}
	if len(items) != 3 {
		t.Fatalf("items=%d", len(items))
	}
	if items[0].Kind != KindDomain || items[1].CIDR != "1.1.1.1/32" || items[2].Port != 443 {
		t.Fatalf("%+v", items)
	}
}

func TestScanAndMutate(t *testing.T) {
	dir := t.TempDir()
	content := `
sniffer:
  force-domain:
    - +.v2ex.com
  skip-domain: []
dns:
  fake-ip-filter:
    - "+.lan"
rule-providers:
  rule_classical_direct:
    type: inline
    behavior: classical
    payload:
      - SRC-PORT,41641
  rule_classical_vip:
    type: inline
    behavior: classical
    payload: []
  rule_classical_proxy:
    type: inline
    behavior: classical
    payload: []
`
	path := filepath.Join(dir, "aio.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: path}
	items, _ := ParseItems("v2ex.com\n41641")
	hits := s.Scan(items)
	if len(hits[0]) == 0 {
		t.Fatal("expected force-domain hit for v2ex")
	}
	if len(hits[1]) == 0 {
		t.Fatal("expected port hits for 41641")
	}

	if err := s.Add(CatProxy, "DOMAIN-SUFFIX,example-test.invalid"); err != nil {
		t.Fatal(err)
	}
	text, _ := s.Read()
	if !contains(text, "DOMAIN-SUFFIX,example-test.invalid") {
		t.Fatal("add failed")
	}
	hits2 := s.Scan([]Item{{Kind: KindDomain, Host: "example-test.invalid", Value: "example-test.invalid"}})
	if len(hits2[0]) != 1 {
		t.Fatalf("scan after add: %v", hits2[0])
	}
	if err := s.DeleteLine(hits2[0][0].Index); err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})()))
}
