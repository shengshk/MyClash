package main

import (
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	modePrefer  = "prefer"
	modeSource = "source"

	awaitSrcName   = "src_name"
	awaitSrcURL    = "src_url"
	awaitSrcRename = "src_rename"
	awaitSrcReURL  = "src_reurl"
)

func (a *App) startPrefer(uid, chatID int64, userMsgID int) {
	a.scrubOld(uid, chatID)
	a.initStore()
	pref, all, err := a.store.PreferredSource()
	if err != nil {
		sent := a.sendText(chatID, "⚠️ "+err.Error())
		a.deleteLater(chatID, userMsgID, 2*time.Second)
		a.deleteLater(chatID, sent, ttlEphemeral)
		return
	}
	sess := &Session{
		Mode:     modePrefer,
		ChatID:   chatID,
		ExpireAt: time.Now().Add(ttlSession),
	}
	a.trackUser(sess, userMsgID)
	a.setSess(uid, sess)
	a.persistSession(uid, sess)
	a.scheduleExpire(uid)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("✈️ 当前优先订阅为「%s」\n点击可切换为：\n", pref.Name))
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, src := range all {
		label := src.Name
		if src.Name == pref.Name {
			label = "⭐ " + src.Name + "（当前）"
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, "pf:"+src.Name),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✖️ 取消", "pf:x"),
	))
	mid := a.sendMarkup(chatID, b.String(), tgbotapi.NewInlineKeyboardMarkup(rows...))
	a.trackBot(sess, mid)
	a.persistSession(uid, sess)
	a.deleteLater(chatID, userMsgID, 2*time.Second)
}

func (a *App) startSources(uid, chatID int64, userMsgID int) {
	a.scrubOld(uid, chatID)
	a.initStore()
	sess := &Session{
		Mode:     modeSource,
		ChatID:   chatID,
		ExpireAt: time.Now().Add(ttlSession),
	}
	a.trackUser(sess, userMsgID)
	a.setSess(uid, sess)
	a.persistSession(uid, sess)
	a.scheduleExpire(uid)
	a.showSourceList(uid, chatID, sess, nil)
	a.deleteLater(chatID, userMsgID, 2*time.Second)
}

func (a *App) showSourceList(uid, chatID int64, sess *Session, cq *tgbotapi.CallbackQuery) {
	all, err := a.store.ListSources()
	if err != nil {
		a.finishAndScrub(uid, chatID, "⚠️ "+err.Error(), ttlEphemeral)
		return
	}
	pref, _, _ := a.store.PreferredSource()
	var b strings.Builder
	b.WriteString("🛠 管理订阅\n当前订阅：\n")
	if len(all) == 0 {
		b.WriteString("（空）\n")
	} else {
		for _, src := range all {
			mark := ""
			if src.Name == pref.Name {
				mark = " ⭐优先"
			}
			b.WriteString(fmt.Sprintf("· %s%s\n", src.Name, mark))
		}
	}
	b.WriteString("\n点选查看详情，或添加新订阅：")
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, src := range all {
		label := src.Name
		if src.Name == pref.Name {
			label = "⭐ " + src.Name
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, "src:v:"+src.Name),
		))
	}
	rows = append(rows,
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("➕ 添加订阅", "src:add")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("✖️ 取消", "src:x")),
	)
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	if cq != nil {
		a.editMarkup(cq, b.String(), kb)
	} else {
		mid := a.sendMarkup(chatID, b.String(), kb)
		a.trackBot(sess, mid)
		a.persistSession(uid, sess)
	}
}

func (a *App) showSourceDetail(cq *tgbotapi.CallbackQuery, sess *Session, name string) {
	all, err := a.store.ListSources()
	if err != nil {
		a.answer(cq, err.Error())
		return
	}
	var src *Source
	for i := range all {
		if all[i].Name == name {
			src = &all[i]
			break
		}
	}
	if src == nil {
		a.answer(cq, "订阅不存在")
		return
	}
	sess.SrcName = name
	a.persistSession(cq.From.ID, sess)
	urlShow := src.URL
	if len(urlShow) > 80 {
		urlShow = urlShow[:77] + "…"
	}
	text := fmt.Sprintf("订阅详情\n名称：%s\nNoHK：%s\n槽位：p%d\n链接：%s\n路径：%s",
		src.Name, src.NoHK(), src.Slot, urlShow, src.Path)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✏️ 编辑", "src:e:"+name),
			tgbotapi.NewInlineKeyboardButtonData("🗑 删除", "src:d:"+name),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️ 返回", "src:list"),
			tgbotapi.NewInlineKeyboardButtonData("✖️ 取消", "src:x"),
		),
	)
	a.editMarkup(cq, text, kb)
}

func (a *App) showSourceEditMenu(cq *tgbotapi.CallbackQuery, name string) {
	text := fmt.Sprintf("编辑「%s」", name)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("修改名称", "src:en:"+name),
			tgbotapi.NewInlineKeyboardButtonData("修改连接", "src:eu:"+name),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️ 返回", "src:v:"+name),
			tgbotapi.NewInlineKeyboardButtonData("✖️ 取消", "src:x"),
		),
	)
	a.editMarkup(cq, text, kb)
}

func (a *App) promptSourceText(uid, chatID int64, sess *Session, cq *tgbotapi.CallbackQuery, await, tip string) {
	sess.AwaitInput = true
	sess.AwaitKind = await
	sess.ExpireAt = time.Now().Add(ttlSession)
	a.persistSession(uid, sess)
	if cq != nil {
		a.edit(cq, tip)
	} else {
		mid := a.sendText(chatID, tip)
		a.trackBot(sess, mid)
		a.persistSession(uid, sess)
	}
}

func (a *App) handleSourceText(uid, chatID int64, sess *Session, text string) {
	switch sess.AwaitKind {
	case awaitSrcName:
		if err := ValidateSourceName(text); err != nil {
			mid := a.sendText(chatID, "⚠️ "+err.Error()+"\n请重新发送名称：")
			a.trackBot(sess, mid)
			a.deleteLater(chatID, mid, ttlEphemeral)
			return
		}
		sess.DraftName = strings.TrimSpace(text)
		sess.AwaitKind = awaitSrcURL
		a.persistSession(uid, sess)
		mid := a.sendText(chatID, fmt.Sprintf("名称「%s」已记下。\n请发送订阅链接（http/https）：", sess.DraftName))
		a.trackBot(sess, mid)
		a.persistSession(uid, sess)

	case awaitSrcURL:
		if err := ValidateSourceURL(text); err != nil {
			mid := a.sendText(chatID, "⚠️ "+err.Error()+"\n请重新发送链接：")
			a.trackBot(sess, mid)
			a.deleteLater(chatID, mid, ttlEphemeral)
			return
		}
		src, err := a.store.AddSource(sess.DraftName, strings.TrimSpace(text))
		if err != nil {
			a.finishAndScrub(uid, chatID, "⚠️ 添加失败："+err.Error(), ttlEphemeral)
			return
		}
		a.finishAndScrub(uid, chatID, fmt.Sprintf("✅ 已添加订阅「%s」（槽位 p%d）\n💾 已自动备份", src.Name, src.Slot), ttlDone)

	case awaitSrcRename:
		old := sess.SrcName
		if err := a.store.RenameSource(old, strings.TrimSpace(text)); err != nil {
			mid := a.sendText(chatID, "⚠️ "+err.Error()+"\n请重新发送名称：")
			a.trackBot(sess, mid)
			a.deleteLater(chatID, mid, ttlEphemeral)
			return
		}
		a.finishAndScrub(uid, chatID, fmt.Sprintf("✅ 已改名：%s → %s\n💾 已自动备份", old, strings.TrimSpace(text)), ttlDone)

	case awaitSrcReURL:
		if err := a.store.UpdateSourceURL(sess.SrcName, strings.TrimSpace(text)); err != nil {
			mid := a.sendText(chatID, "⚠️ "+err.Error()+"\n请重新发送链接：")
			a.trackBot(sess, mid)
			a.deleteLater(chatID, mid, ttlEphemeral)
			return
		}
		a.finishAndScrub(uid, chatID, fmt.Sprintf("✅ 已更新「%s」订阅链接\n💾 已自动备份", sess.SrcName), ttlDone)

	default:
		a.finishAndScrub(uid, chatID, "⚠️ 会话状态异常", ttlEphemeral)
	}
}

func (a *App) handlePreferCallback(cq *tgbotapi.CallbackQuery, sess *Session, data string) bool {
	if !strings.HasPrefix(data, "pf:") {
		return false
	}
	uid := cq.From.ID
	chatID := cq.Message.Chat.ID
	arg := strings.TrimPrefix(data, "pf:")
	if arg == "x" {
		a.answer(cq, "已取消")
		a.finishAndScrub(uid, chatID, "已取消", ttlEphemeral)
		return true
	}
	if err := a.store.SetPreferred(arg); err != nil {
		a.answer(cq, err.Error())
		return true
	}
	a.answer(cq, "已切换")
	a.finishAndScrub(uid, chatID, fmt.Sprintf("✅ 优先订阅已切换为「%s」\n💾 已自动备份", arg), ttlDone)
	return true
}

func (a *App) handleSourceCallback(cq *tgbotapi.CallbackQuery, sess *Session, data string) bool {
	if !strings.HasPrefix(data, "src:") {
		return false
	}
	uid := cq.From.ID
	chatID := cq.Message.Chat.ID
	rest := strings.TrimPrefix(data, "src:")

	switch {
	case rest == "x":
		a.answer(cq, "已取消")
		a.finishAndScrub(uid, chatID, "已取消", ttlEphemeral)
		return true
	case rest == "list":
		a.answer(cq, "")
		sess.AwaitInput = false
		sess.AwaitKind = ""
		a.showSourceList(uid, chatID, sess, cq)
		return true
	case rest == "add":
		a.answer(cq, "")
		sess.DraftName = ""
		a.promptSourceText(uid, chatID, sess, cq, awaitSrcName,
			"➕ 添加订阅\n请发送订阅名称（字母数字 _ -）：")
		return true
	case rest == "inh":
		// 顺位继承：已经是删除后列表顺序，无需再写
		a.answer(cq, "已顺位继承")
		next := sess.DraftName // stashed next prefer name
		a.finishAndScrub(uid, chatID, fmt.Sprintf("✅ 已顺位继承优先订阅为「%s」", next), ttlDone)
		return true
	case rest == "man":
		a.answer(cq, "")
		// 进入优先订阅选择
		a.scrubOld(uid, chatID)
		a.startPrefer(uid, chatID, 0)
		return true
	case strings.HasPrefix(rest, "v:"):
		a.answer(cq, "")
		a.showSourceDetail(cq, sess, strings.TrimPrefix(rest, "v:"))
		return true
	case strings.HasPrefix(rest, "e:"):
		a.answer(cq, "")
		a.showSourceEditMenu(cq, strings.TrimPrefix(rest, "e:"))
		return true
	case strings.HasPrefix(rest, "en:"):
		name := strings.TrimPrefix(rest, "en:")
		sess.SrcName = name
		a.answer(cq, "")
		a.promptSourceText(uid, chatID, sess, cq, awaitSrcRename,
			fmt.Sprintf("修改「%s」名称\n请发送新名称：", name))
		return true
	case strings.HasPrefix(rest, "eu:"):
		name := strings.TrimPrefix(rest, "eu:")
		sess.SrcName = name
		a.answer(cq, "")
		a.promptSourceText(uid, chatID, sess, cq, awaitSrcReURL,
			fmt.Sprintf("修改「%s」订阅链接\n请发送新 URL：", name))
		return true
	case strings.HasPrefix(rest, "d:"):
		name := strings.TrimPrefix(rest, "d:")
		a.answer(cq, "")
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("确认删除", "src:dx:"+name),
				tgbotapi.NewInlineKeyboardButtonData("取消", "src:v:"+name),
			),
		)
		a.editMarkup(cq, fmt.Sprintf("确认删除订阅「%s」？\n将移除登记表 / 链接 / 策略组引用。", name), kb)
		return true
	case strings.HasPrefix(rest, "dx:"):
		name := strings.TrimPrefix(rest, "dx:")
		wasPref, next, err := a.store.DeleteSource(name)
		if err != nil {
			a.answer(cq, err.Error())
			return true
		}
		a.answer(cq, "已删除")
		if wasPref && next != "" {
			sess.Mode = modeSource
			sess.DraftName = next
			sess.ExpireAt = time.Now().Add(ttlSession)
			a.setSess(uid, sess)
			a.persistSession(uid, sess)
			text := fmt.Sprintf("🗑 已删除「%s」\n原优先订阅被删除，默认顺位继承为「%s」。\n是否需要指定优先订阅？", name, next)
			kb := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("顺位继承", "src:inh"),
					tgbotapi.NewInlineKeyboardButtonData("手动指定", "src:man"),
				),
			)
			a.editMarkup(cq, text, kb)
			return true
		}
		a.finishAndScrub(uid, chatID, fmt.Sprintf("🗑 已删除订阅「%s」\n💾 已自动备份", name), ttlDone)
		return true
	}
	return false
}
