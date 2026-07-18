package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type scrubItem struct {
	UID      int64 `json:"uid"`
	ChatID   int64 `json:"chatId"`
	UserMsgs []int `json:"userMsgs"`
	BotMsgs  []int `json:"botMsgs"`
	ExpireAt int64 `json:"expireAt"` // unix seconds
}

func (a *App) scrubPath() string {
	dir := filepath.Dir(a.cfg.YAMLPath)
	return filepath.Join(dir, ".myclash_scrub.json")
}

func (a *App) persistSession(uid int64, sess *Session) {
	if sess == nil {
		a.persistRemove(uid)
		return
	}
	item := scrubItem{
		UID:      uid,
		ChatID:   sess.ChatID,
		UserMsgs: append([]int{}, sess.UserMsgs...),
		BotMsgs:  append([]int{}, sess.BotMsgs...),
		ExpireAt: sess.ExpireAt.Unix(),
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	items := a.loadScrubLocked()
	out := make([]scrubItem, 0, len(items)+1)
	for _, it := range items {
		if it.UID != uid {
			out = append(out, it)
		}
	}
	out = append(out, item)
	a.saveScrubLocked(out)
}

func (a *App) persistRemove(uid int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	items := a.loadScrubLocked()
	out := items[:0]
	for _, it := range items {
		if it.UID != uid {
			out = append(out, it)
		}
	}
	a.saveScrubLocked(out)
}

func (a *App) loadScrubLocked() []scrubItem {
	b, err := os.ReadFile(a.scrubPath())
	if err != nil || len(b) == 0 {
		return nil
	}
	var items []scrubItem
	if json.Unmarshal(b, &items) != nil {
		return nil
	}
	return items
}

func (a *App) saveScrubLocked(items []scrubItem) {
	path := a.scrubPath()
	if len(items) == 0 {
		_ = os.Remove(path)
		return
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func (a *App) resumePendingScrubs() {
	a.mu.Lock()
	items := a.loadScrubLocked()
	a.mu.Unlock()
	if len(items) == 0 {
		return
	}
	now := time.Now()
	for _, it := range items {
		uid := it.UID
		chatID := it.ChatID
		userMsgs := append([]int{}, it.UserMsgs...)
		botMsgs := append([]int{}, it.BotMsgs...)
		exp := time.Unix(it.ExpireAt, 0)

		if now.After(exp) {
			log.Printf("scrub resume: delete overdue uid=%d msgs", uid)
			for _, id := range userMsgs {
				a.deleteMsg(chatID, id)
			}
			for _, id := range botMsgs {
				a.deleteMsg(chatID, id)
			}
			a.persistRemove(uid)
			continue
		}
		// 恢复会话外壳，仅用于超时清理
		sess := &Session{
			ChatID:   chatID,
			UserMsgs: userMsgs,
			BotMsgs:  botMsgs,
			ExpireAt: exp,
		}
		a.setSess(uid, sess)
		a.scheduleExpire(uid)
		log.Printf("scrub resume: watch uid=%d until %s", uid, exp.Format(time.RFC3339))
	}
}

// ensureExpireRunner 保证每个 uid 只有一个超时巡检 goroutine
func (a *App) ensureExpireRunner(uid int64) {
	if a.expireRun == nil {
		a.expireRun = &sync.Map{}
	}
	if _, loaded := a.expireRun.LoadOrStore(uid, true); loaded {
		return
	}
	go a.expireLoop(uid)
}

func (a *App) expireLoop(uid int64) {
	defer a.expireRun.Delete(uid)
	for {
		sess := a.getSess(uid)
		if sess == nil {
			a.persistRemove(uid)
			return
		}
		wait := time.Until(sess.ExpireAt)
		if wait > 0 {
			timer := time.NewTimer(wait + 150*time.Millisecond)
			<-timer.C
			continue
		}
		chatID := sess.ChatID
		a.deleteTracked(sess)
		a.clearSess(uid)
		a.persistRemove(uid)
		mid := a.sendText(chatID, "⏱ 操作超时，已清理对话")
		a.deleteLater(chatID, mid, ttlEphemeral)
		log.Printf("scrub expire: cleaned uid=%d chat=%d", uid, chatID)
		return
	}
}
