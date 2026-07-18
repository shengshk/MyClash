package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleAIO = `# --- 锚点配置 ---
x-p1:  &p1  YYG
x-p1n: &p1n YYG-NoHK
x-p2:  &p2  LXY
x-p2n: &p2n LXY-NoHK
x-p3:  &p3  SLY
x-p3n: &p3n SLY-NoHK

x-provider:           &provider           { type: http, interval: 3600 }
x-url-test:           &url-test           { type: url-test, url: "https://www.gstatic.com/generate_204", interval: 60, hidden: true }
x-proxy-NoHK:         &proxy-NoHK         [*p1n, *p2n, *p3n, DIRECT, ♻️ 永不失联, 🖐 手动选择]
x-proxy:              &proxy              [*p1, *p2, *p3, DIRECT, ♻️ 永不失联, 🖐 手动选择]

proxy-providers:
  YYG:
    <<: *provider
    url: https://example.com/yyg
    path: ./providers/YYG.yaml
  LXY:
    <<: *provider
    url: https://example.com/lxy
    path: ./providers/LXY.yaml
  SLY:
    <<: *provider
    url: https://example.com/sly
    path: ./providers/SLY.yaml

proxy-groups:
  - {name: 🖐 手动选择, type: select, proxies: [*p1n, *p2n, *p3n, *p1, *p2, *p3], include-all-providers: true}
  - {name: ♻️ 永不失联, <<: *fallback, proxies: [*p1, *p2, *p3], hidden: true}
  # 2. 订阅基础分组（name/use 跟登记表走）
  - {name: *p1n, <<: *url-test, use: [*p1], filter: *no-hongkong}
  - {name: *p2n, <<: *url-test, use: [*p2], filter: *no-hongkong}
  - {name: *p3n, <<: *url-test, use: [*p3], filter: *no-hongkong}
  - {name: *p1, <<: *url-test, use: [*p1]}
  - {name: *p2, <<: *url-test, use: [*p2]}
  - {name: *p3, <<: *url-test, use: [*p3]}

  # 3. 地区专项组（备忘）
`

func writeSample(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "aio.yaml")
	if err := os.WriteFile(path, []byte(sampleAIO), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "providers"), 0o755)
	return &Store{Path: path}
}

func TestParseAndPrefer(t *testing.T) {
	s := writeSample(t)
	all, err := s.ListSources()
	if err != nil || len(all) != 3 {
		t.Fatalf("list=%v err=%v", all, err)
	}
	pref, _, err := s.PreferredSource()
	if err != nil || pref.Name != "YYG" {
		t.Fatalf("pref=%+v err=%v", pref, err)
	}
	if err := s.SetPreferred("LXY"); err != nil {
		t.Fatal(err)
	}
	pref, _, _ = s.PreferredSource()
	if pref.Name != "LXY" {
		t.Fatalf("after set pref=%s", pref.Name)
	}
	text, _ := s.Read()
	if !strings.Contains(text, "proxies: [*p2, *p1, *p3]") {
		t.Fatalf("prefer order not updated:\n%s", text)
	}
}

func TestDeleteBackfillAdd(t *testing.T) {
	s := writeSample(t)
	was, next, err := s.DeleteSource("LXY") // p2
	if err != nil {
		t.Fatal(err)
	}
	if was {
		t.Fatal("LXY was not preferred")
	}
	_ = next
	all, _ := s.ListSources()
	if len(all) != 2 {
		t.Fatalf("all=%+v", all)
	}
	if lowestFreeSlot(all) != 2 {
		t.Fatalf("free slot want 2 got %d", lowestFreeSlot(all))
	}
	src, err := s.AddSource("NEWCO", "https://example.com/new")
	if err != nil {
		t.Fatal(err)
	}
	if src.Slot != 2 {
		t.Fatalf("backfill slot=%d", src.Slot)
	}
	text, _ := s.Read()
	if !strings.Contains(text, "x-p2:  &p2  NEWCO") {
		t.Fatalf("registry missing:\n%s", text)
	}
	if !strings.Contains(text, "  NEWCO:") || !strings.Contains(text, "url: https://example.com/new") {
		t.Fatalf("provider missing:\n%s", text)
	}
	if !strings.Contains(text, "*p2") || !strings.Contains(text, "name: *p2n") {
		t.Fatalf("groups/lists missing p2:\n%s", text)
	}
}

func TestDeletePreferredPromptData(t *testing.T) {
	s := writeSample(t)
	was, next, err := s.DeleteSource("YYG")
	if err != nil || !was || next != "LXY" {
		t.Fatalf("was=%v next=%s err=%v", was, next, err)
	}
}

func TestRenameAndURL(t *testing.T) {
	s := writeSample(t)
	if err := s.RenameSource("SLY", "ORANGE"); err != nil {
		t.Fatal(err)
	}
	text, _ := s.Read()
	if strings.Contains(text, "x-p3:  &p3  SLY") || !strings.Contains(text, "x-p3:  &p3  ORANGE") {
		t.Fatalf("rename reg:\n%s", text)
	}
	if !strings.Contains(text, "  ORANGE:") || strings.Contains(text, "\n  SLY:") {
		t.Fatalf("rename provider:\n%s", text)
	}
	if err := s.UpdateSourceURL("ORANGE", "https://example.com/orange2"); err != nil {
		t.Fatal(err)
	}
	text, _ = s.Read()
	if !strings.Contains(text, "url: https://example.com/orange2") {
		t.Fatalf("url:\n%s", text)
	}
}

func TestValidate(t *testing.T) {
	if ValidateSourceName("bad name") == nil {
		t.Fatal("space")
	}
	if ValidateSourceURL("ftp://x") == nil {
		t.Fatal("ftp")
	}
	if err := ValidateSourceName("YYG_1"); err != nil {
		t.Fatal(err)
	}
}
