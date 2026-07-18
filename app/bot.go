package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	ttlEphemeral = 8 * time.Second  // 提示/错误，短消失
	ttlDone      = 4 * time.Second  // 操作成功摘要
	ttlSession   = 5 * time.Minute  // 超时未操作清理
)

type App struct {
	bot       *tgbotapi.BotAPI
	cfg       *Config
	store     *Store
	backup    *BackupStore
	sub       *SubHub
	mu        sync.Mutex
	sess      map[int64]*Session
	expireRun *sync.Map
}

func (a *App) initStore() {
	if a.store == nil {
		a.store = &Store{Path: a.cfg.YAMLPath}
	}
}

type Session struct {
	Items      []Item
	Hits       map[int][]Hit
	Action     string // add | del
	ItemIdx    int
	Cat        Category
	FixedCat   Category // 固定命令引导新增
	AwaitInput bool     // 等待用户按格式输入
	Guided     bool     // 固定命令批量新增中
	Mode       string   // prefer | source | ""
	AwaitKind  string   // src_name | src_url | src_rename | src_reurl
	SrcName     string   // 正在编辑的订阅名
	DraftName  string   // 添加中的名称 / 删除后顺位名
	DomMode    string   // exact | suffix
	DomHost    string
	DomOpts    []DomainOption
	Dir        string // SRC/DST / IP dir
	DirSet     bool   // 已选过方向（IP-CIDR 时 Dir 可为空）
	ChatID     int64
	UserMsgs   []int
	BotMsgs    []int
	ExpireAt   time.Time
}

var catEmoji = map[Category]string{
	CatForceSniff: "🔎",
	CatSkipSniff:  "🔇",
	CatRealIP:     "🎯",
	CatDirect:     "➡️",
	CatVip:        "⭐",
	CatProxy:      "🚀",
}

func catTitle(c Category) string {
	return catEmoji[c] + " " + catLabel[c]
}

var cmdToCat = map[string]Category{
	"force_sniff": CatForceSniff,
	"skip_sniff":  CatSkipSniff,
	"real_ip":     CatRealIP,
	"direct":      CatDirect,
	"vip":         CatVip,
	"proxy":       CatProxy,
}

func (a *App) registerCommands() {
	// 说明统一「emoji + 六个字」
	cmds := []tgbotapi.BotCommand{
		{Command: "panel", Description: "📲 打开操作面板"},
		{Command: "prior", Description: "✈️ 切换优先订阅"},
		{Command: "subs", Description: "🛠 管理订阅列表"},
		{Command: "links", Description: "📡 获取综合订阅"},
		{Command: "renew", Description: "🔄 重置综合链接"},
		{Command: "backup", Description: "💾 备份与恢复"},
		{Command: "guide", Description: "❓ 查看使用说明"},
	}
	if _, err := a.bot.Request(tgbotapi.NewSetMyCommands(cmds...)); err != nil {
		log.Printf("set commands: %v", err)
	}
}

func (a *App) allowed(id int64) bool {
	return a.cfg.AllowIDs[id]
}

func (a *App) handleMessage(m *tgbotapi.Message) {
	a.initStore()
	chatID := m.Chat.ID
	uid := m.From.ID

	if !a.allowed(uid) {
		sent := a.sendText(chatID, "🚫 无权限")
		a.deleteLater(chatID, m.MessageID, 2*time.Second)
		if sent != 0 {
			a.deleteLater(chatID, sent, ttlEphemeral)
		}
		return
	}

	text := strings.TrimSpace(m.Text)
	cmd, cmdArgs := splitCommand(text)

	switch cmd {
	case "start", "panel", "menu": // menu 兼容旧习惯
		a.deleteLater(chatID, m.MessageID, 1*time.Second)
		sent := a.sendMainMenu(chatID, cmd == "start")
		a.deleteLater(chatID, sent, 30*time.Second)
		return
	case "guide", "help":
		a.deleteLater(chatID, m.MessageID, 1*time.Second)
		sent := a.sendText(chatID, helpText)
		a.deleteLater(chatID, sent, 60*time.Second)
		return
	case "prior", "prefer":
		a.startPrefer(uid, chatID, m.MessageID)
		return
	case "subs":
		a.startSources(uid, chatID, m.MessageID)
		return
	case "links":
		a.sendSubLinks(uid, chatID, m.MessageID, false)
		return
	case "renew":
		a.sendSubLinks(uid, chatID, m.MessageID, true)
		return
	case "backup":
		a.startBackup(uid, chatID, m.MessageID)
		return
	}

	// 底部面板按钮
	if a.handlePanelButton(uid, chatID, m.MessageID, text) {
		return
	}

	if cat, ok := cmdToCat[cmd]; ok {
		a.startGuided(uid, chatID, m.MessageID, cat, cmdArgs)
		return
	}

	// 等待文本输入（规则新增 / 订阅编辑）
	if sess := a.getSess(uid); sess != nil && sess.AwaitInput {
		a.trackUser(sess, m.MessageID)
		if sess.FixedCat != "" {
			a.handleGuidedInput(uid, chatID, sess, text)
			return
		}
		if sess.Mode == modeSource {
			a.handleSourceText(uid, chatID, sess, text)
			return
		}
	}

	// 自由识别流程
	items, errs := ParseItems(text)
	if len(items) == 0 {
		msg := "😕 没有可识别的条目"
		if len(errs) > 0 {
			msg += "\n" + strings.Join(errs, "\n")
		}
		msg += "\n\n" + panelHelpHint() + "\n或发 /panel 打开操作面板"
		sent := a.sendText(chatID, msg)
		a.deleteLater(chatID, m.MessageID, 2*time.Second)
		a.deleteLater(chatID, sent, ttlEphemeral)
		return
	}

	a.beginFreeform(uid, chatID, m.MessageID, items, errs)
}

func splitCommand(text string) (cmd, args string) {
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	body := strings.TrimSpace(strings.TrimPrefix(text, "/"))
	parts := strings.SplitN(body, " ", 2)
	name := parts[0]
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	if len(parts) == 2 {
		args = strings.TrimSpace(parts[1])
	}
	return name, args
}

func (a *App) startGuided(uid, chatID int64, userMsgID int, cat Category, inlineArgs string) {
	a.scrubOld(uid, chatID)
	sess := &Session{
		FixedCat:   cat,
		AwaitInput: true,
		ChatID:     chatID,
		ExpireAt:   time.Now().Add(ttlSession),
	}
	a.trackUser(sess, userMsgID)
	a.setSess(uid, sess)
	a.persistSession(uid, sess)
	a.scheduleExpire(uid)

	// 允许 /cmd google.com 一步到位
	if inlineArgs != "" {
		a.deleteLater(chatID, userMsgID, 2*time.Second)
		a.handleGuidedInput(uid, chatID, sess, inlineArgs)
		return
	}

	guide := guidedPrompt(cat)
	mid := a.sendText(chatID, guide)
	a.trackBot(sess, mid)
	a.persistSession(uid, sess)
	a.deleteLater(chatID, userMsgID, 2*time.Second)
}

func guidedPrompt(cat Category) string {
	base := fmt.Sprintf("%s\n请发送要【新增】的内容（多条请换行）\n\n", catTitle(cat))
	switch cat {
	case CatForceSniff, CatSkipSniff, CatRealIP:
		return base + "格式示例：\n• google.com\n• +.example.com\n• 1.1.1.1 或 1.1.1.1/（自动 /32）"
	default:
		return base + "格式示例：\n• google.com\n• +.example.com\n• 1.1.1.1 或 1.1.1.1/\n• 也可贴 URL（自动拆域名/端口）"
	}
}

func (a *App) handleGuidedInput(uid, chatID int64, sess *Session, text string) {
	items, errs := ParseItems(text)
	if len(items) == 0 {
		msg := "😕 无法识别，请按提示重新发送"
		if len(errs) > 0 {
			msg += "\n" + strings.Join(errs, "\n")
		}
		mid := a.sendText(chatID, msg)
		a.deleteLater(chatID, mid, ttlEphemeral)
		return
	}

	for _, it := range items {
		if it.Kind == KindPort && isListCat(sess.FixedCat) {
			mid := a.sendText(chatID, fmt.Sprintf("⚠️ 端口不能写入「%s」，请换规则类命令（ /direct /vip /proxy ）", catTitle(sess.FixedCat)))
			a.deleteLater(chatID, mid, ttlEphemeral)
			return
		}
	}

	sess.Items = items
	sess.Hits = a.store.Scan(items)
	sess.Action = "add"
	sess.Cat = sess.FixedCat
	sess.AwaitInput = false
	sess.Guided = true
	sess.ItemIdx = 0
	sess.DomMode = ""
	sess.DomHost = ""
	sess.Dir = ""
	sess.DirSet = false
	sess.ExpireAt = time.Now().Add(ttlSession)
	a.persistSession(uid, sess)

	var note string
	if len(errs) > 0 {
		note = "⚠️ 已跳过：\n" + strings.Join(errs, "\n")
	}
	if note != "" {
		mid := a.sendText(chatID, note)
		a.trackBot(sess, mid)
		a.deleteLater(chatID, mid, ttlEphemeral)
	}

	// 多个候选：先选意图（单选），避免域名+端口一起写
	if len(items) > 1 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%s\n🔎 解析到多个候选，请选择意图：\n", catTitle(sess.Cat)))
		for i, it := range items {
			b.WriteString(fmt.Sprintf("\n%d) %s（%s）", i+1, it.Value, kindName(it.Kind)))
		}
		var rows [][]tgbotapi.InlineKeyboardButton
		for i, it := range items {
			label := fmt.Sprintf("%s %s", kindEmoji(it.Kind), truncate(it.Value, 40))
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("intent:%d", i)),
			))
		}
		mid := a.sendMarkup(chatID, b.String(), tgbotapi.NewInlineKeyboardMarkup(rows...))
		a.trackBot(sess, mid)
		a.persistSession(uid, sess)
		return
	}

	a.continueAdd(uid, chatID, sess, nil)
}

func (a *App) beginFreeform(uid, chatID int64, userMsgID int, items []Item, errs []string) {
	a.scrubOld(uid, chatID)
	hits := a.store.Scan(items)
	sess := &Session{
		Items:    items,
		Hits:     hits,
		ItemIdx:  -1,
		ChatID:   chatID,
		ExpireAt: time.Now().Add(ttlSession),
	}
	a.trackUser(sess, userMsgID)
	a.setSess(uid, sess)
	a.persistSession(uid, sess)
	a.scheduleExpire(uid)

	totalHits := 0
	var b strings.Builder
	b.WriteString("🔎 已解析候选：\n")
	for i, it := range items {
		b.WriteString(fmt.Sprintf("\n%d) %s（%s）\n", i+1, it.Value, kindName(it.Kind)))
		hs := hits[i]
		if len(hs) == 0 {
			b.WriteString("   · 📭 未匹配到已有项\n")
			continue
		}
		totalHits += len(hs)
		for _, h := range hs {
			b.WriteString(fmt.Sprintf("   · ✅ %s → %s\n", catTitle(h.Cat), h.Line))
		}
	}
	if len(errs) > 0 {
		b.WriteString("\n⚠️ 跳过：\n")
		b.WriteString(strings.Join(errs, "\n"))
	}

	// 多个候选：先单选意图，再选新增/删除
	if len(items) > 1 {
		b.WriteString("\n请选择意图（单选）：")
		var rows [][]tgbotapi.InlineKeyboardButton
		for i, it := range items {
			label := fmt.Sprintf("%s %s", kindEmoji(it.Kind), truncate(it.Value, 40))
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("intent:%d", i)),
			))
		}
		mid := a.sendMarkup(chatID, b.String(), tgbotapi.NewInlineKeyboardMarkup(rows...))
		a.trackBot(sess, mid)
		a.persistSession(uid, sess)
		return
	}

	b.WriteString("\n请选择操作：")
	row := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("➕ 新增", "act:add"),
	}
	if totalHits > 0 {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData("🗑 删除", "act:del"))
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(row)

	mid := a.sendMarkup(chatID, b.String(), kb)
	a.trackBot(sess, mid)
	a.persistSession(uid, sess)
}

func kindEmoji(k Kind) string {
	switch k {
	case KindDomain:
		return "🌐"
	case KindIP:
		return "📍"
	case KindPort:
		return "🔌"
	}
	return "•"
}

func (a *App) askActionAfterIntent(cq *tgbotapi.CallbackQuery, sess *Session) {
	it := sess.Items[sess.ItemIdx]
	hs := sess.Hits[sess.ItemIdx]
	var b strings.Builder
	b.WriteString(fmt.Sprintf("已选：%s（%s）\n", it.Value, kindName(it.Kind)))
	if len(hs) == 0 {
		b.WriteString("📭 未匹配到已有项\n")
	} else {
		for _, h := range hs {
			b.WriteString(fmt.Sprintf("· ✅ %s → %s\n", catTitle(h.Cat), h.Line))
		}
	}
	b.WriteString("\n请选择操作：")
	row := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("➕ 新增", "act:add"),
	}
	if len(hs) > 0 {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData("🗑 删除", "act:del"))
	}
	a.editMarkup(cq, b.String(), tgbotapi.NewInlineKeyboardMarkup(row))
}

func kindName(k Kind) string {
	switch k {
	case KindDomain:
		return "域名"
	case KindIP:
		return "IP"
	case KindPort:
		return "端口"
	}
	return "?"
}

func (a *App) handleCallback(cq *tgbotapi.CallbackQuery) {
	a.initStore()
	if cq.From == nil || !a.allowed(cq.From.ID) {
		a.answer(cq, "无权限")
		return
	}
	data := cq.Data
	chatID := cq.Message.Chat.ID
	uid := cq.From.ID
	sess := a.getSess(uid)
	if sess == nil {
		a.answer(cq, "⏱ 会话已过期，请重新发送")
		a.deleteLater(chatID, cq.Message.MessageID, 2*time.Second)
		return
	}
	sess.ExpireAt = time.Now().Add(ttlSession)
	a.trackBot(sess, cq.Message.MessageID)
	a.persistSession(uid, sess)

	if a.handlePreferCallback(cq, sess, data) {
		return
	}
	if a.handleSourceCallback(cq, sess, data) {
		return
	}
	if a.handleRuleMenuCallback(cq, sess, data) {
		return
	}
	if a.handleBackupCallback(cq, sess, data) {
		return
	}

	switch {
	case strings.HasPrefix(data, "intent:"):
		idx, _ := strconv.Atoi(strings.TrimPrefix(data, "intent:"))
		if idx < 0 || idx >= len(sess.Items) {
			a.answer(cq, "无效候选")
			return
		}
		// 收成单项，避免后续误把其它候选一并写入
		it := sess.Items[idx]
		sess.Items = []Item{it}
		sess.Hits = a.store.Scan(sess.Items)
		sess.ItemIdx = 0
		sess.DomMode = ""
		sess.DomHost = ""
		sess.Dir = ""
		sess.DirSet = false
		a.answer(cq, "")
		if sess.Guided && sess.Cat != "" {
			a.continueAdd(uid, chatID, sess, cq)
			return
		}
		a.askActionAfterIntent(cq, sess)
		return

	case data == "act:add", data == "act:del":
		if data == "act:del" {
			if sess.ItemIdx >= 0 {
				if len(sess.Hits[sess.ItemIdx]) == 0 {
					a.answer(cq, "没有可删项")
					return
				}
			} else if countHits(sess) == 0 {
				a.answer(cq, "没有可删项")
				return
			}
		}
		sess.Action = strings.TrimPrefix(data, "act:")
		a.answer(cq, "")
		// 已在意图步骤选定 ItemIdx 时，跳过再选条目
		if sess.ItemIdx >= 0 && sess.ItemIdx < len(sess.Items) {
			if sess.Action == "del" {
				a.askDeleteTargets(cq, sess)
			} else {
				a.askCategory(cq, sess)
			}
			return
		}
		a.askItem(cq, sess)
		return

	case strings.HasPrefix(data, "item:"):
		idx, _ := strconv.Atoi(strings.TrimPrefix(data, "item:"))
		if idx < 0 || idx >= len(sess.Items) {
			a.answer(cq, "无效条目")
			return
		}
		sess.ItemIdx = idx
		a.answer(cq, "")
		if sess.Action == "del" {
			a.askDeleteTargets(cq, sess)
		} else {
			a.askCategory(cq, sess)
		}
		return

	case strings.HasPrefix(data, "cat:"):
		sess.Cat = Category(strings.TrimPrefix(data, "cat:"))
		sess.DomMode = ""
		sess.DomHost = ""
		sess.Dir = ""
		sess.DirSet = false
		a.answer(cq, "")
		a.continueAdd(uid, chatID, sess, cq)
		return

	case strings.HasPrefix(data, "dom:"):
		idx, err := strconv.Atoi(strings.TrimPrefix(data, "dom:"))
		if err != nil || idx < 0 || idx >= len(sess.DomOpts) {
			a.answer(cq, "无效选项")
			return
		}
		opt := sess.DomOpts[idx]
		sess.DomMode = opt.Mode
		sess.DomHost = opt.Host
		a.answer(cq, "")
		a.continueAdd(uid, chatID, sess, cq)
		return

	case strings.HasPrefix(data, "pdir:"):
		sess.Dir = strings.TrimPrefix(data, "pdir:")
		sess.DirSet = true
		a.answer(cq, "")
		a.continueAdd(uid, chatID, sess, cq)
		return

	case strings.HasPrefix(data, "idir:"):
		sess.Dir = strings.TrimPrefix(data, "idir:")
		sess.DirSet = true
		a.answer(cq, "")
		a.continueAdd(uid, chatID, sess, cq)
		return

	case data == "gadd:":
		a.answer(cq, "")
		a.commitAdd(uid, cq, sess)
		return

	case strings.HasPrefix(data, "del:"):
		lineIdx, err := strconv.Atoi(strings.TrimPrefix(data, "del:"))
		if err != nil {
			a.answer(cq, "无效")
			return
		}
		if err := a.store.DeleteLine(lineIdx); err != nil {
			a.answer(cq, "")
			a.finishAndScrub(uid, chatID, "⚠️ 删除失败: "+err.Error(), ttlEphemeral)
			return
		}
		a.answer(cq, "已删除")
		a.finishAndScrub(uid, chatID, fmt.Sprintf("🗑 已删除（原第 %d 行）\n💾 已自动备份", lineIdx+1), ttlDone)
		return
	}

	a.answer(cq, "未知操作")
}

func countHits(sess *Session) int {
	n := 0
	for _, hs := range sess.Hits {
		n += len(hs)
	}
	return n
}

func needsPortDir(it Item, cat Category) bool {
	return it.Kind == KindPort && isRuleCat(cat)
}

func needsIPDir(it Item, cat Category) bool {
	return it.Kind == KindIP && isRuleCat(cat)
}

func (a *App) askItem(cq *tgbotapi.CallbackQuery, sess *Session) {
	if len(sess.Items) == 1 {
		sess.ItemIdx = 0
		if sess.Action == "del" {
			a.askDeleteTargets(cq, sess)
		} else {
			a.askCategory(cq, sess)
		}
		return
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for i, it := range sess.Items {
		if sess.Action == "del" && len(sess.Hits[i]) == 0 {
			continue
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(
				fmt.Sprintf("📌 %d) %s", i+1, truncate(it.Value, 36)),
				fmt.Sprintf("item:%d", i),
			),
		))
	}
	if len(rows) == 0 {
		a.edit(cq, "📭 没有可删除的匹配项")
		a.deleteLater(cq.Message.Chat.ID, cq.Message.MessageID, ttlEphemeral)
		a.clearSess(cq.From.ID)
		return
	}
	title := "➕ 选择要新增的条目："
	if sess.Action == "del" {
		title = "🗑 选择要删除的相关条目："
	}
	a.editMarkup(cq, title, tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (a *App) askDeleteTargets(cq *tgbotapi.CallbackQuery, sess *Session) {
	hits := sess.Hits[sess.ItemIdx]
	if len(hits) == 0 {
		a.edit(cq, "📭 该项没有匹配，无法删除")
		return
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, h := range hits {
		label := fmt.Sprintf("%s %s", catTitle(h.Cat), truncate(h.Line, 28))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("del:%d", h.Index)),
		))
	}
	a.editMarkup(cq, "🗑 删除哪一项？", tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (a *App) askCategory(cq *tgbotapi.CallbackQuery, sess *Session) {
	it := sess.Items[sess.ItemIdx]
	var cats []Category
	switch it.Kind {
	case KindPort:
		cats = append([]Category{}, ruleCats...)
	default:
		cats = append(append([]Category{}, listCats...), ruleCats...)
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, c := range cats {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(catTitle(c), "cat:"+string(c)),
		))
	}
	a.editMarkup(cq, fmt.Sprintf("➕ 新增 %s 到哪里？", it.Value), tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func portDirKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📥 SRC-PORT", "pdir:SRC"),
			tgbotapi.NewInlineKeyboardButtonData("📤 DST-PORT", "pdir:DST"),
		),
	)
}

func ipDirKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("IP-CIDR", "idir:"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("SRC-IP-CIDR", "idir:SRC"),
			tgbotapi.NewInlineKeyboardButtonData("DST-IP-CIDR", "idir:DST"),
		),
	)
}



func (a *App) writeSpec(sess *Session) WriteSpec {
	return WriteSpec{
		Cat:     sess.Cat,
		Dir:     sess.Dir,
		DomMode: sess.DomMode,
		DomHost: sess.DomHost,
	}
}

// continueAdd 推进：域名粒度 → 端口/IP 方向 → 预览确认
func (a *App) continueAdd(uid, chatID int64, sess *Session, cq *tgbotapi.CallbackQuery) {
	if sess.ItemIdx < 0 || sess.ItemIdx >= len(sess.Items) {
		a.finishAndScrub(uid, chatID, "⚠️ 内部状态错误", ttlEphemeral)
		return
	}
	it := sess.Items[sess.ItemIdx]

	// 1) 域名粒度：二级域（如 qqssl.fun）默认 DOMAIN-SUFFIX；更深主机再问精确/后缀
	if it.Kind == KindDomain && !strings.Contains(it.Host, " ") && sess.DomMode == "" {
		host := strings.ToLower(it.Host)
		if strings.HasPrefix(strings.TrimSpace(it.Raw), "+.") || domainLabelCount(host) == 2 {
			sess.DomMode = "suffix"
			sess.DomHost = host
		} else {
			opts := BuildDomainOptions(it)
			if len(opts) == 0 {
				sess.DomMode = "exact"
				sess.DomHost = host
			} else {
				sess.DomOpts = opts
				a.askDomainMode(uid, chatID, sess, cq, it, opts)
				return
			}
		}
	}
	if it.Kind == KindDomain && strings.Contains(it.Host, " ") && sess.DomMode == "" {
		sess.DomMode = "exact"
		sess.DomHost = it.Host
	}

	// 2) 端口 / IP 方向
	if needsPortDir(it, sess.Cat) && !sess.DirSet {
		text := "🔌 端口规则方向："
		if cq != nil {
			a.editMarkup(cq, text, portDirKeyboard())
		} else {
			mid := a.sendMarkup(chatID, text, portDirKeyboard())
			a.trackBot(sess, mid)
			a.persistSession(uid, sess)
		}
		return
	}
	if needsIPDir(it, sess.Cat) && !sess.DirSet {
		text := "🌐 IP 规则类型："
		if cq != nil {
			a.editMarkup(cq, text, ipDirKeyboard())
		} else {
			mid := a.sendMarkup(chatID, text, ipDirKeyboard())
			a.trackBot(sess, mid)
			a.persistSession(uid, sess)
		}
		return
	}

	a.showAddPreview(uid, chatID, sess, cq)
}

func (a *App) askDomainMode(uid, chatID int64, sess *Session, cq *tgbotapi.CallbackQuery, it Item, opts []DomainOption) {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📐 匹配粒度（写入 %s）\n原文：%s\n\n", catTitle(sess.Cat), it.Value))
	if sess.Guided && len(sess.Items) > 1 {
		b.WriteString(fmt.Sprintf("进度 %d/%d\n\n", sess.ItemIdx+1, len(sess.Items)))
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for i, o := range opts {
		prefix := "○ "
		if o.Rec {
			prefix = "⭐ "
		}
		btn := prefix + o.Label
		if o.Hint != "" {
			btn = truncate(btn+" · "+o.Hint, 60)
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(btn, fmt.Sprintf("dom:%d", i)),
		))
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	if cq != nil {
		a.editMarkup(cq, b.String(), kb)
	} else {
		mid := a.sendMarkup(chatID, b.String(), kb)
		a.trackBot(sess, mid)
		a.persistSession(uid, sess)
	}
}

func (a *App) showAddPreview(uid, chatID int64, sess *Session, cq *tgbotapi.CallbackQuery) {
	it := sess.Items[sess.ItemIdx]
	spec := a.writeSpec(sess)
	entry, err := FormatEntry(it, spec)
	if err != nil {
		if cq != nil {
			a.edit(cq, "⚠️ "+err.Error())
		} else {
			mid := a.sendText(chatID, "⚠️ "+err.Error())
			a.deleteLater(chatID, mid, ttlEphemeral)
		}
		return
	}

	rep := a.store.AnalyzeConflicts(entry, sess.Cat)
	var b strings.Builder
	b.WriteString("👁 写入预览\n")
	b.WriteString(fmt.Sprintf("目标：%s\n", catTitle(sess.Cat)))
	b.WriteString(fmt.Sprintf("内容：%s\n", entry))
	if sess.Guided && len(sess.Items) > 1 {
		b.WriteString(fmt.Sprintf("进度：%d/%d\n", sess.ItemIdx+1, len(sess.Items)))
	}

	if rep.SameCat != nil {
		msg := fmt.Sprintf("👁 写入预览\n目标：%s\n内容：%s\n\n⛔ 目标类已存在同款：\n· %s\n无需重复写入。",
			catTitle(sess.Cat), entry, rep.SameCat.Line)
		a.finishAndScrub(uid, chatID, msg, ttlEphemeral)
		return
	}

	if len(rep.Migrate) > 0 {
		b.WriteString("\n🔀 将自动从互斥类移除同款：\n")
		for _, h := range rep.Migrate {
			b.WriteString(fmt.Sprintf("· %s → %s\n", catTitle(h.Cat), h.Line))
		}
	}
	if len(rep.ShadowedBy) > 0 {
		b.WriteString("\n⚠️ 可能被更靠前规则盖住（直连→VIP→普通）：\n")
		for _, h := range rep.ShadowedBy {
			b.WriteString(fmt.Sprintf("· %s → %s\n", catTitle(h.Cat), h.Line))
		}
	}
	if len(rep.Shadows) > 0 {
		b.WriteString("\n⚠️ 写入后可能盖住后方规则：\n")
		for _, h := range rep.Shadows {
			b.WriteString(fmt.Sprintf("· %s → %s\n", catTitle(h.Cat), h.Line))
		}
	}

	b.WriteString("\n确认？")
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ 确认写入", "gadd:"),
		),
	)
	if cq != nil {
		a.editMarkup(cq, b.String(), kb)
	} else {
		mid := a.sendMarkup(chatID, b.String(), kb)
		a.trackBot(sess, mid)
		a.persistSession(uid, sess)
	}
}

func (a *App) commitAdd(uid int64, cq *tgbotapi.CallbackQuery, sess *Session) {
	chatID := cq.Message.Chat.ID
	it := sess.Items[sess.ItemIdx]
	spec := a.writeSpec(sess)
	entry, err := FormatEntry(it, spec)
	if err != nil {
		a.finishAndScrub(uid, chatID, "⚠️ "+err.Error(), ttlEphemeral)
		return
	}
	rep := a.store.AnalyzeConflicts(entry, sess.Cat)
	if rep.SameCat != nil {
		a.finishAndScrub(uid, chatID, fmt.Sprintf("⛔ 已存在于 %s：%s", catTitle(sess.Cat), rep.SameCat.Line), ttlEphemeral)
		return
	}
	if err := a.store.AddMigrating(sess.Cat, entry, rep.Migrate); err != nil {
		a.finishAndScrub(uid, chatID, "⚠️ 新增失败: "+err.Error(), ttlEphemeral)
		return
	}

	var extra strings.Builder
	if len(rep.Migrate) > 0 {
		extra.WriteString("\n🔀 已移除同款：")
		for _, h := range rep.Migrate {
			extra.WriteString(fmt.Sprintf("\n· %s → %s", catTitle(h.Cat), h.Line))
		}
	}

	if sess.Guided && sess.ItemIdx+1 < len(sess.Items) {
		sess.ItemIdx++
		sess.DomMode = ""
		sess.DomHost = ""
		sess.Dir = ""
		sess.DirSet = false
		sess.DomOpts = nil
		notice := fmt.Sprintf("✅ 已写入：%s%s\n继续下一项…", entry, extra.String())
		a.edit(cq, notice)
		a.continueAdd(uid, chatID, sess, cq)
		return
	}

	a.finishAndScrub(uid, chatID,
		fmt.Sprintf("✅ 已新增到 %s\n%s%s\n💾 已自动备份", catTitle(sess.Cat), entry, extra.String()), ttlDone)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (a *App) trackUser(sess *Session, id int) {
	if id == 0 {
		return
	}
	for _, x := range sess.UserMsgs {
		if x == id {
			return
		}
	}
	sess.UserMsgs = append(sess.UserMsgs, id)
}

func (a *App) trackBot(sess *Session, id int) {
	if id == 0 {
		return
	}
	for _, x := range sess.BotMsgs {
		if x == id {
			return
		}
	}
	sess.BotMsgs = append(sess.BotMsgs, id)
}


func (a *App) setSess(uid int64, s *Session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sess[uid] = s
}

func (a *App) getSess(uid int64) *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess[uid]
}

func (a *App) clearSess(uid int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sess, uid)
}

func (a *App) scrubOld(uid, chatID int64) {
	sess := a.getSess(uid)
	if sess == nil {
		return
	}
	a.deleteTracked(sess)
	a.clearSess(uid)
	a.persistRemove(uid)
}

func (a *App) deleteTracked(sess *Session) {
	if sess == nil {
		return
	}
	for _, id := range sess.UserMsgs {
		a.deleteMsg(sess.ChatID, id)
	}
	for _, id := range sess.BotMsgs {
		a.deleteMsg(sess.ChatID, id)
	}
}

func (a *App) finishAndScrub(uid, chatID int64, summary string, keep time.Duration) {
	sess := a.getSess(uid)
	a.clearSess(uid)
	a.persistRemove(uid)
	if sess != nil {
		a.deleteTracked(sess)
	}
	if summary == "" {
		return
	}
	mid := a.sendText(chatID, summary)
	a.deleteLater(chatID, mid, keep)
}

func (a *App) scheduleExpire(uid int64) {
	a.ensureExpireRunner(uid)
}

func (a *App) sendText(chatID int64, text string) int {
	msg := tgbotapi.NewMessage(chatID, text)
	sent, err := a.bot.Send(msg)
	if err != nil {
		log.Printf("send: %v", err)
		return 0
	}
	return sent.MessageID
}

func (a *App) sendMarkup(chatID int64, text string, kb tgbotapi.InlineKeyboardMarkup) int {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = kb
	sent, err := a.bot.Send(msg)
	if err != nil {
		log.Printf("sendMarkup: %v", err)
		return 0
	}
	return sent.MessageID
}

func (a *App) deleteMsg(chatID int64, msgID int) {
	if msgID == 0 {
		return
	}
	if _, err := a.bot.Request(tgbotapi.NewDeleteMessage(chatID, msgID)); err != nil {
		log.Printf("deleteMsg chat=%d id=%d: %v", chatID, msgID, err)
	}
}

func (a *App) deleteLater(chatID int64, msgID int, after time.Duration) {
	if msgID == 0 {
		return
	}
	go func() {
		time.Sleep(after)
		a.deleteMsg(chatID, msgID)
	}()
}

func (a *App) answer(cq *tgbotapi.CallbackQuery, text string) {
	cb := tgbotapi.NewCallback(cq.ID, text)
	if _, err := a.bot.Request(cb); err != nil {
		log.Printf("callback: %v", err)
	}
}

func (a *App) edit(cq *tgbotapi.CallbackQuery, text string) {
	edit := tgbotapi.NewEditMessageText(cq.Message.Chat.ID, cq.Message.MessageID, text)
	if _, err := a.bot.Send(edit); err != nil {
		log.Printf("edit: %v", err)
	}
}

func (a *App) editMarkup(cq *tgbotapi.CallbackQuery, text string, kb tgbotapi.InlineKeyboardMarkup) {
	edit := tgbotapi.NewEditMessageTextAndMarkup(cq.Message.Chat.ID, cq.Message.MessageID, text, kb)
	if _, err := a.bot.Send(edit); err != nil {
		log.Printf("editMarkup: %v", err)
	}
}

const helpText = `📖 myclash

推荐：底部操作面板（/panel 打开）
· 📋 新增规则 → 选类型后发送内容
· ✈️ 优先订阅 → 切换永不失联优先
· 🛠 管理订阅 → 增删改订阅源
· 💾 备份恢复 → 手动备份 / 列表恢复
· 📡 综合订阅 → 综合配置链接
· 🔄 重置综合 → 作废旧链并获取新链
· 也可直接发送域名 / IP / URL

命令：/panel /prior /subs /links /renew /backup /guide

说明：
• 链接会拆成主机 / 父域 / 端口供单选
• 二级域默认 DOMAIN-SUFFIX
• 订阅显示短名，不用 p1/p2

⏱ 操作约 5 分钟超时；消息会自动清理`
