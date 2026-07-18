package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubHubPerDomainTokens(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "aio.yaml")
	if err := os.WriteFile(yamlPath, []byte("proxies: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	domains := []string{"https://a.example", "https://b.example", "http://10.0.0.2:8006"}
	h, err := newSubHub(yamlPath, domains)
	if err != nil {
		t.Fatal(err)
	}
	links := h.Links()
	if len(links) != 3 {
		t.Fatalf("links=%v", links)
	}
	seen := map[string]bool{}
	for _, l := range links {
		parts := strings.Split(l, "/")
		tok := parts[len(parts)-1]
		if seen[tok] {
			t.Fatalf("duplicate token in %v", links)
		}
		seen[tok] = true
	}
	h2, err := newSubHub(yamlPath, domains)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(h2.Links(), "|") != strings.Join(links, "|") {
		t.Fatalf("persist mismatch")
	}
	old := append([]string{}, links...)
	if err := h.Rotate(); err != nil {
		t.Fatal(err)
	}
	neu := h.Links()
	for i := range old {
		if old[i] == neu[i] {
			t.Fatalf("link %d unchanged after rotate", i)
		}
		oldTok := strings.Split(old[i], "/")
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+oldTok[len(oldTok)-1], nil)
		h.serve(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("old token still valid")
		}
	}
	newTok := strings.Split(neu[0], "/")
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/"+newTok[len(newTok)-1], nil)
	h.serve(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("new token serve %d", rr2.Code)
	}
}
