package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	subListenDefault = ":8006"
	subTokenFileName = ".sub_tokens.json"
	subTokenLegacy   = ".sub_token"
	subTokenBytes    = 24 // → 48 hex chars
)

type SubHub struct {
	mu        sync.RWMutex
	yamlPath  string
	tokenPath string
	domains   []string          // ordered bases
	tokens    map[string]string // domain base -> token
}

func newSubHub(yamlPath string, domains []string) (*SubHub, error) {
	bases := make([]string, 0, len(domains))
	for _, d := range domains {
		b := normalizeDomain(d)
		if b != "" {
			bases = append(bases, b)
		}
	}
	h := &SubHub{
		yamlPath:  yamlPath,
		tokenPath: filepath.Join(filepath.Dir(yamlPath), subTokenFileName),
		domains:   bases,
		tokens:    map[string]string{},
	}
	if err := h.loadOrCreateTokens(); err != nil {
		return nil, err
	}
	return h, nil
}

func normalizeDomain(d string) string {
	return strings.TrimRight(strings.TrimSpace(d), "/")
}

func (h *SubHub) loadOrCreateTokens() error {
	needSave := false
	if b, err := os.ReadFile(h.tokenPath); err == nil && len(b) > 0 {
		var m map[string]string
		if json.Unmarshal(b, &m) == nil && len(m) > 0 {
			for k, v := range m {
				nk := normalizeDomain(k)
				if nk != "" && isSafeToken(v) {
					h.tokens[nk] = v
				}
			}
		}
	}
	// ensure every current domain has its own token
	for _, d := range h.domains {
		if tok, ok := h.tokens[d]; ok && isSafeToken(tok) {
			continue
		}
		tok, err := randomToken()
		if err != nil {
			return err
		}
		// avoid accidental collision with another domain's token
		for h.tokenInUse(tok) {
			tok, err = randomToken()
			if err != nil {
				return err
			}
		}
		h.tokens[d] = tok
		needSave = true
	}
	// drop tokens for domains no longer configured
	alive := map[string]bool{}
	for _, d := range h.domains {
		alive[d] = true
	}
	for k := range h.tokens {
		if !alive[k] {
			delete(h.tokens, k)
			needSave = true
		}
	}
	if len(h.tokens) == 0 {
		return h.rotateAllLocked()
	}
	if needSave {
		return h.saveLocked()
	}
	return nil
}

func (h *SubHub) tokenInUse(tok string) bool {
	for _, t := range h.tokens {
		if t == tok {
			return true
		}
	}
	return false
}

func isSafeToken(s string) bool {
	if len(s) < 16 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func (h *SubHub) Domains() []string {
	return append([]string{}, h.domains...)
}

func (h *SubHub) Links() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	for _, d := range h.domains {
		tok := h.tokens[d]
		if d == "" || tok == "" {
			continue
		}
		out = append(out, d+"/"+tok)
	}
	return out
}

func (h *SubHub) Rotate() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rotateAllLocked()
}

func (h *SubHub) rotateAllLocked() error {
	next := map[string]string{}
	used := map[string]bool{}
	for _, d := range h.domains {
		for {
			tok, err := randomToken()
			if err != nil {
				return err
			}
			if used[tok] {
				continue
			}
			used[tok] = true
			next[d] = tok
			break
		}
	}
	h.tokens = next
	return h.saveLocked()
}

func (h *SubHub) saveLocked() error {
	b, err := json.MarshalIndent(h.tokens, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.WriteFile(h.tokenPath, b, 0o600); err != nil {
		return err
	}
	// remove legacy single-token file if present
	_ = os.Remove(filepath.Join(filepath.Dir(h.tokenPath), subTokenLegacy))
	return nil
}

func randomToken() (string, error) {
	b := make([]byte, subTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}

func (h *SubHub) StartHTTP(addr string) {
	if addr == "" {
		addr = subListenDefault
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.serve)
	go func() {
		log.Printf("sub server listen %s (token file %s, %d domains)", addr, h.tokenPath, len(h.domains))
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("sub server: %v", err)
		}
	}()
}

func (h *SubHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	path = strings.Trim(path, "/")
	if path == "" || strings.Contains(path, "/") || !isSafeToken(path) {
		http.NotFound(w, r)
		return
	}
	h.mu.RLock()
	ok := false
	for _, tok := range h.tokens {
		if tok == path {
			ok = true
			break
		}
	}
	h.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(h.yamlPath)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Profile-Update-Interval", "24")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(data)
}

func loadDomainsFromEnv() []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		v = normalizeDomain(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	if v := os.Getenv("DOMAIN"); v != "" {
		add(v)
	}
	for i := 1; i <= 20; i++ {
		add(os.Getenv(fmt.Sprintf("DOMAIN%d", i)))
	}
	return out
}
