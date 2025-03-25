package bot

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"csv2/ranking"
	"csv2/utils"
	"github.com/bwmarrin/discordgo"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func Start(discordToken, telegramToken, telegramChatID, floodChannelID, relayChannelID string, rank *ranking.Ranking) {
	dg := SetupDiscord(discordToken, floodChannelID, relayChannelID, rank)
	defer dg.Close()

	tgBot, chatID := setupTelegram(telegramToken, telegramChatID)

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID || m.ChannelID != relayChannelID {
			return
		}

		// Если есть текст без вложений
		if m.Content != "" && len(m.Attachments) == 0 {
			escapedContent := utils.EscapeMarkdownV2(m.Content)
			escapedUsername := utils.EscapeMarkdownV2(m.Author.Username)
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("🎧:\n*%s*: %s", escapedUsername, escapedContent))
			msg.ParseMode = "MarkdownV2"
			if _, err := tgBot.Send(msg); err != nil {
				log.Printf("Failed to send message to Telegram: %v", err)
			}
		}

		// Если есть вложения (с текстом или без)
		if len(m.Attachments) > 0 {
			for _, attachment := range m.Attachments {
				caption := ""
				if m.Content != "" {
					caption = fmt.Sprintf("🎧:\n%s: %s", m.Author.Username, m.Content)
				} else {
					caption = fmt.Sprintf("🎧:\n%s:", m.Author.Username)
				}

				// Скачиваем файл локально
				filePath := fmt.Sprintf("content/file_%d_%s", time.Now().UnixNano(), attachment.Filename)
				if err := utils.DownloadFile(attachment.URL, filePath); err != nil {
					log.Printf("Failed to download file from Discord: %v", err)
					continue
				}

				// Проверяем тип вложения
				if strings.HasPrefix(attachment.ContentType, "image/") {
					photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
					photo.Caption = caption
					if _, err := tgBot.Send(photo); err != nil {
						log.Printf("Failed to send image to Telegram: %v", err)
					}
				} else {
					doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
					doc.Caption = caption
					if _, err := tgBot.Send(doc); err != nil {
						log.Printf("Failed to send document to Telegram: %v", err)
					}
				}

				// Удаляем временный файл
				if err := os.Remove(filePath); err != nil {
					log.Printf("Failed to remove temporary file: %v", err)
				}
			}
		}
	})

	go handleTelegramUpdates(tgBot, chatID, dg, relayChannelID)
	select {}
}
