package main

import (
	"testing"
)

func TestParseURLDomainPort(t *testing.T) {
	items, errs := ParseItems("https://isyn.qqssl.fun:51888/s/x3BwwCZpQa92tryC")
	if len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
	want := map[string]Kind{
		"isyn.qqssl.fun": KindDomain,
		"qqssl.fun":      KindDomain,
		"51888":          KindPort,
	}
	if len(items) != len(want) {
		t.Fatalf("items=%+v", items)
	}
	for _, it := range items {
		k, ok := want[it.Value]
		if !ok || k != it.Kind {
			t.Fatalf("unexpected %+v", it)
		}
	}
}

func TestParseURLIPPort(t *testing.T) {
	items, errs := ParseItems("http://10.0.0.2:9001/s/x3BwwCZpQa92tryC")
	if len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Kind != KindIP || items[0].CIDR != "10.0.0.2/32" {
		t.Fatalf("ip=%+v", items[0])
	}
	if items[1].Kind != KindPort || items[1].Port != 9001 {
		t.Fatalf("port=%+v", items[1])
	}
}

func TestParseBareDomainParents(t *testing.T) {
	items, _ := ParseItems("mail.google.com")
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Value != "mail.google.com" || items[1].Value != "google.com" {
		t.Fatalf("items=%+v", items)
	}
}

func TestParseRejectsRawURLAsDomain(t *testing.T) {
	items, errs := ParseItems("https://isyn.qqssl.fun:51888/s/xxx")
	for _, it := range items {
		if stringsContains(it.Value, "://") || stringsContains(it.Value, "/") {
			t.Fatalf("raw url leaked as value: %+v", it)
		}
	}
	if len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
