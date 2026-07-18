package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const modeBackup = "backup"

func (a *App) backups() *BackupStore {
	if a.backup == nil {
		a.backup = newBackupStore(a.cfg.YAMLPath, a.cfg.BackupMax)
	}
	return a.backup
}

func (a *App) startBackup(uid, chatID int64, userMsgID int) {
	a.scrubOld(uid, chatID)
	a.initStore()
	sess := &Session{
		Mode:     modeBackup,
		ChatID:   chatID,
		ExpireAt: time.Now().Add(ttlSession),
	}
	a.trackUser(sess, userMsgID)
	a.setSess(uid, sess)
	a.persistSession(uid, sess)
	a.scheduleExpire(uid)
	a.showBackupMenu(uid, chatID, sess, nil)
	a.deleteLater(chatID, userMsgID, 2*time.Second)
}

func (a *App) showBackupMenu(uid, chatID int64, sess *Session, cq *tgbotapi.CallbackQuery) {
	list, _ := a.backups().List()
	text := fmt.Sprintf("💾 备份与恢复\n当前 %d/%d 份（满则自动删最旧）\n", len(list), a.backups().Max)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📦 立即备份", "bk:now"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📋 备份列表", "bk:list"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✖️ 取消", "bk:x"),
		),
	)
	if cq != nil {
		a.editMarkup(cq, text, kb)
		a.persistSession(uid, sess)
		return
	}
	mid := a.sendMarkup(chatID, text, kb)
	a.trackBot(sess, mid)
	a.persistSession(uid, sess)
}

func (a *App) showBackupList(uid, chatID int64, sess *Session, cq *tgbotapi.CallbackQuery) {
	list, err := a.backups().List()
	if err != nil {
		a.answer(cq, err.Error())
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📋 备份列表 %d/%d\n点选查看：\n", len(list), a.backups().Max))
	var rows [][]tgbotapi.InlineKeyboardButton
	if len(list) == 0 {
		b.WriteString("（暂无备份）\n")
	}
	for _, m := range list {
		label := fmt.Sprintf("%s · %s", m.Time.Format("01-02 15:04"), kindLabel(m.Kind))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, "bk:v:"+m.ID),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("⬅️ 返回", "bk:menu"),
		tgbotapi.NewInlineKeyboardButtonData("✖️ 取消", "bk:x"),
	))
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	if cq != nil {
		a.editMarkup(cq, b.String(), kb)
	} else {
		mid := a.sendMarkup(chatID, b.String(), kb)
		a.trackBot(sess, mid)
	}
	a.persistSession(uid, sess)
}

func (a *App) showBackupDetail(uid, chatID int64, sess *Session, cq *tgbotapi.CallbackQuery, id string) {
	m, err := a.backups().Get(id)
	if err != nil {
		a.answer(cq, err.Error())
		return
	}
	text := fmt.Sprintf("备份详情\n时间：%s\n类型：%s\n大小：%d KB\n",
		m.Time.Format("2006-01-02 15:04:05"), kindLabel(m.Kind), m.Size/1024)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("♻️ 恢复", "bk:r:"+m.ID),
			tgbotapi.NewInlineKeyboardButtonData("🗑 删除", "bk:d:"+m.ID),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️ 返回", "bk:list"),
			tgbotapi.NewInlineKeyboardButtonData("✖️ 取消", "bk:x"),
		),
	)
	a.editMarkup(cq, text, kb)
	a.persistSession(uid, sess)
}

func (a *App) handleBackupCallback(cq *tgbotapi.CallbackQuery, sess *Session, data string) bool {
	if !strings.HasPrefix(data, "bk:") {
		return false
	}
	uid := cq.From.ID
	chatID := cq.Message.Chat.ID
	arg := strings.TrimPrefix(data, "bk:")

	switch {
	case arg == "x":
		a.answer(cq, "已取消")
		a.finishAndScrub(uid, chatID, "已取消", ttlEphemeral)
		return true
	case arg == "menu":
		a.answer(cq, "")
		a.showBackupMenu(uid, chatID, sess, cq)
		return true
	case arg == "list":
		a.answer(cq, "")
		a.showBackupList(uid, chatID, sess, cq)
		return true
	case arg == "now":
		m, err := a.backups().Create(backupKindManual)
		if err != nil {
			a.answer(cq, "备份失败")
			a.editMarkup(cq, "⚠️ 备份失败："+err.Error(), tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ 返回", "bk:menu")),
			))
			return true
		}
		a.answer(cq, "已备份")
		list, _ := a.backups().List()
		a.finishAndScrub(uid, chatID,
			fmt.Sprintf("✅ 已备份（手动）\n时间 %s\n当前 %d/%d",
				m.Time.Format("01-02 15:04"), len(list), a.backups().Max),
			ttlDone)
		return true
	case strings.HasPrefix(arg, "v:"):
		a.answer(cq, "")
		a.showBackupDetail(uid, chatID, sess, cq, strings.TrimPrefix(arg, "v:"))
		return true
	case strings.HasPrefix(arg, "r:"):
		id := strings.TrimPrefix(arg, "r:")
		m, err := a.backups().Get(id)
		if err != nil {
			a.answer(cq, err.Error())
			return true
		}
		a.answer(cq, "")
		text := fmt.Sprintf("确认恢复到此备份？\n%s · %s\n将覆盖当前 YAML。",
			m.Time.Format("01-02 15:04"), kindLabel(m.Kind))
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ 确认恢复", "bk:rok:"+id),
				tgbotapi.NewInlineKeyboardButtonData("⬅️ 返回", "bk:v:"+id),
			),
		)
		a.editMarkup(cq, text, kb)
		return true
	case strings.HasPrefix(arg, "rok:"):
		id := strings.TrimPrefix(arg, "rok:")
		if err := a.backups().Restore(id); err != nil {
			a.answer(cq, "恢复失败")
			a.finishAndScrub(uid, chatID, "⚠️ 恢复失败："+err.Error(), ttlEphemeral)
			return true
		}
		a.answer(cq, "已恢复")
		a.finishAndScrub(uid, chatID, "✅ 已恢复该备份\n如使用 OpenClash/Mihomo，请自行重载配置", ttlDone)
		return true
	case strings.HasPrefix(arg, "d:"):
		id := strings.TrimPrefix(arg, "d:")
		m, err := a.backups().Get(id)
		if err != nil {
			a.answer(cq, err.Error())
			return true
		}
		a.answer(cq, "")
		text := fmt.Sprintf("确认删除备份？\n%s · %s", m.Time.Format("01-02 15:04"), kindLabel(m.Kind))
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ 确认删除", "bk:dok:"+id),
				tgbotapi.NewInlineKeyboardButtonData("⬅️ 返回", "bk:v:"+id),
			),
		)
		a.editMarkup(cq, text, kb)
		return true
	case strings.HasPrefix(arg, "dok:"):
		id := strings.TrimPrefix(arg, "dok:")
		if err := a.backups().Delete(id); err != nil {
			a.answer(cq, "删除失败")
			return true
		}
		a.answer(cq, "已删除")
		a.showBackupList(uid, chatID, sess, cq)
		return true
	}
	a.answer(cq, "未知操作")
	return true
}

func (a *App) startBackupCron() {
	min, hour, everyDays, err := parseBackupCron(a.cfg.BackupCron)
	if err != nil {
		log.Printf("backup cron disabled: %v", err)
		return
	}
	log.Printf("backup cron: every %d day(s) at %02d:%02d (TZ local), max=%d", everyDays, hour, min, a.cfg.BackupMax)
	go func() {
		for {
			now := time.Now()
			next := nextBackupTime(now, min, hour, everyDays, a.backups().lastAutoTime())
			log.Printf("backup next run at %s", next.Format(time.RFC3339))
			timer := time.NewTimer(time.Until(next))
			<-timer.C
			m, err := a.backups().Create(backupKindAuto)
			if err != nil {
				log.Printf("auto backup: %v", err)
				continue
			}
			log.Printf("auto backup ok id=%s", m.ID)
		}
	}()
}
