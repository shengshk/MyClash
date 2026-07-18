package main

import (
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// 底部 Reply Keyboard：统一「emoji + 四个字」
const (
	btnRules    = "📋 新增规则"
	btnPrefer   = "✈️ 优先订阅"
	btnSources = "🛠 管理订阅"
	btnSubGet   = "📡 综合订阅"
	btnSubRenew = "🔄 重置综合"
	btnBackup   = "💾 备份恢复"
	btnHelp     = "❓ 使用说明"
	btnHideKB   = "⌨️ 收起键盘"
	btnShowKB   = "📲 打开面板"
)

func mainReplyKeyboard() tgbotapi.ReplyKeyboardMarkup {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnSubGet),
			tgbotapi.NewKeyboardButton(btnSubRenew),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnSources),
			tgbotapi.NewKeyboardButton(btnPrefer),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnRules),
			tgbotapi.NewKeyboardButton(btnBackup),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnHelp),
			tgbotapi.NewKeyboardButton(btnHideKB),
		),
	)
	kb.ResizeKeyboard = true
	kb.OneTimeKeyboard = false
	return kb
}

func (a *App) sendMainMenu(chatID int64, withIntro bool) int {
	text := "📲 底部操作面板已打开"
	if withIntro {
		text = "👋 myclash\n用底部按钮操作即可，也可直接发送域名/IP/URL。\n\n" + text
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = mainReplyKeyboard()
	sent, err := a.bot.Send(msg)
	if err != nil {
		return 0
	}
	return sent.MessageID
}

func (a *App) hideReplyKeyboard(chatID int64) int {
	msg := tgbotapi.NewMessage(chatID, "已收起底部面板。需要时发 /panel 或「"+btnShowKB+"」。")
	msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
	sent, err := a.bot.Send(msg)
	if err != nil {
		return 0
	}
	return sent.MessageID
}

func (a *App) showRulesPicker(uid, chatID int64, userMsgID int) {
	a.scrubOld(uid, chatID)
	sess := &Session{
		ChatID:   chatID,
		ExpireAt: time.Now().Add(ttlSession),
	}
	a.trackUser(sess, userMsgID)
	a.setSess(uid, sess)
	a.persistSession(uid, sess)
	a.scheduleExpire(uid)

	text := "📋 选择要【新增】的规则类型："
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, c := range append(append([]Category{}, listCats...), ruleCats...) {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(catTitle(c), "rule:"+string(c)),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✖️ 取消", "rule:x"),
	))
	mid := a.sendMarkup(chatID, text, tgbotapi.NewInlineKeyboardMarkup(rows...))
	a.trackBot(sess, mid)
	a.persistSession(uid, sess)
	a.deleteLater(chatID, userMsgID, 2*time.Second)
}

func (a *App) handleRuleMenuCallback(cq *tgbotapi.CallbackQuery, sess *Session, data string) bool {
	if !strings.HasPrefix(data, "rule:") {
		return false
	}
	uid := cq.From.ID
	chatID := cq.Message.Chat.ID
	arg := strings.TrimPrefix(data, "rule:")
	if arg == "x" {
		a.answer(cq, "已取消")
		a.finishAndScrub(uid, chatID, "已取消", ttlEphemeral)
		return true
	}
	cat := Category(arg)
	if _, ok := catLabel[cat]; !ok {
		a.answer(cq, "无效类型")
		return true
	}
	a.answer(cq, "")
	a.deleteTracked(sess)
	a.clearSess(uid)
	a.persistRemove(uid)
	a.startGuided(uid, chatID, cq.Message.MessageID, cat, "")
	return true
}

func (a *App) handlePanelButton(uid, chatID int64, userMsgID int, text string) bool {
	switch text {
	case btnRules:
		a.showRulesPicker(uid, chatID, userMsgID)
		return true
	case btnPrefer, "优先订阅":
		a.startPrefer(uid, chatID, userMsgID)
		return true
	case btnSources, "管理订阅", "订阅管理":
		a.startSources(uid, chatID, userMsgID)
		return true
	case btnSubGet, "综合订阅", "获取订阅":
		a.sendSubLinks(uid, chatID, userMsgID, false)
		return true
	case btnSubRenew, "重置综合", "重置订阅", "刷新订阅":
		a.sendSubLinks(uid, chatID, userMsgID, true)
		return true
	case btnBackup, "备份恢复", "备份与恢复":
		a.startBackup(uid, chatID, userMsgID)
		return true
	case btnHelp:
		a.deleteLater(chatID, userMsgID, 1*time.Second)
		sent := a.sendText(chatID, helpText)
		a.deleteLater(chatID, sent, 60*time.Second)
		return true
	case btnHideKB:
		a.deleteLater(chatID, userMsgID, 1*time.Second)
		sent := a.hideReplyKeyboard(chatID)
		a.deleteLater(chatID, sent, 8*time.Second)
		return true
	case btnShowKB:
		a.deleteLater(chatID, userMsgID, 1*time.Second)
		sent := a.sendMainMenu(chatID, false)
		a.deleteLater(chatID, sent, 8*time.Second)
		return true
	}
	return false
}

func panelHelpHint() string {
	return fmt.Sprintf("底部面板：%s / %s / %s", btnRules, btnPrefer, btnSources)
}
