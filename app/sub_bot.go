package main

import (
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (a *App) sendSubLinks(uid, chatID int64, userMsgID int, rotate bool) {
	a.deleteLater(chatID, userMsgID, 2*time.Second)
	if a.sub == nil {
		sent := a.sendText(chatID, "⚠️ 未配置综合订阅（缺少 DOMAIN*）")
		a.deleteLater(chatID, sent, ttlEphemeral)
		return
	}
	if rotate {
		if err := a.sub.Rotate(); err != nil {
			sent := a.sendText(chatID, "⚠️ 重置失败："+err.Error())
			a.deleteLater(chatID, sent, ttlEphemeral)
			return
		}
	}
	links := a.sub.Links()
	if len(links) == 0 {
		sent := a.sendText(chatID, "⚠️ 没有可用的 DOMAIN 配置")
		a.deleteLater(chatID, sent, ttlEphemeral)
		return
	}
	prefix := "获取到"
	if rotate {
		prefix = "已重置，获取到"
	}
	notice := a.sendText(chatID, fmt.Sprintf("%s %d 条综合连接：", prefix, len(links)))
	a.deleteLater(chatID, notice, 15*time.Second)

	for _, link := range links {
		msg := tgbotapi.NewMessage(chatID, link)
		msg.DisableWebPagePreview = true
		sent, err := a.bot.Send(msg)
		if err != nil {
			log.Printf("send sub link: %v", err)
			continue
		}
		_ = uid
		_ = sent
		// 纯链接消息不自动删除，方便复制
	}
}
