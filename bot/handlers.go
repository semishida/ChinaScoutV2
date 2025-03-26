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

	// Обработчик сообщений из Discord
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID {
			return
		}

		if m.ChannelID == floodChannelID && strings.HasPrefix(m.Content, "!") {
			log.Printf("Received command: %s from %s in flood channel", m.Content, m.Author.ID)
			handleCommands(s, m, rank)
			return
		}

		if m.ChannelID == relayChannelID {
			log.Printf("Relaying message from Discord: %s from %s", m.Content, m.Author.ID)
			// Текст без вложений
			if m.Content != "" && len(m.Attachments) == 0 {
				escapedContent := utils.EscapeMarkdownV2(m.Content)
				escapedUsername := utils.EscapeMarkdownV2(m.Author.Username)
				msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("🎧:\n*%s*: %s", escapedUsername, escapedContent))
				msg.ParseMode = "MarkdownV2"
				if _, err := tgBot.Send(msg); err != nil {
					log.Printf("Failed to send message to Telegram: %v", err)
				}
			}

			// Вложения
			if len(m.Attachments) > 0 {
				for _, attachment := range m.Attachments {
					caption := fmt.Sprintf("🎧:\n%s:", m.Author.Username)
					if m.Content != "" {
						caption = fmt.Sprintf("🎧:\n%s: %s", m.Author.Username, m.Content)
					}

					filePath := fmt.Sprintf("content/file_%d_%s", time.Now().UnixNano(), attachment.Filename)
					if err := utils.DownloadFile(attachment.URL, filePath); err != nil {
						log.Printf("Failed to download attachment: %v", err)
						continue
					}

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
					os.Remove(filePath)
				}
			}
		}
	})

	go handleTelegramUpdates(tgBot, chatID, dg, relayChannelID)
	select {}
}

func setupTelegram(token, chatID string) (*tgbotapi.BotAPI, int64) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Failed to initialize Telegram bot: %v", err)
	}
	bot.Debug = true
	log.Printf("Authorized on Telegram account %s", bot.Self.UserName)

	parsedChatID, err := utils.ParseChatID(chatID)
	if err != nil {
		log.Fatalf("Invalid Telegram Chat ID: %v", err)
	}

	return bot, parsedChatID
}

func handleTelegramUpdates(bot *tgbotapi.BotAPI, chatID int64, dg *discordgo.Session, relayChannelID string) {
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60
	updates := bot.GetUpdatesChan(updateConfig)

	for update := range updates {
		if update.Message == nil || update.Message.Chat.ID != chatID {
			continue
		}

		log.Printf("Received Telegram message from %s: %s", update.Message.From.UserName, update.Message.Text)

		// Текст без вложений
		if update.Message.Text != "" && update.Message.Photo == nil && update.Message.VideoNote == nil && update.Message.Voice == nil && update.Message.Document == nil {
			msg := fmt.Sprintf("➤ \n**%s**: %s", update.Message.From.UserName, update.Message.Text)
			_, err := dg.ChannelMessageSend(relayChannelID, msg)
			if err != nil {
				log.Printf("Failed to send text message to Discord: %v", err)
			}
		}

		// Фото
		if len(update.Message.Photo) > 0 {
			photoFileID := update.Message.Photo[len(update.Message.Photo)-1].FileID
			fileURL, err := bot.GetFileDirectURL(photoFileID)
			if err != nil {
				log.Printf("Failed to get photo URL: %v", err)
				continue
			}

			photoPath := fmt.Sprintf("content/photo_%d.jpg", time.Now().UnixNano())
			if err := utils.DownloadFile(fileURL, photoPath); err != nil {
				log.Printf("Failed to download photo: %v", err)
				continue
			}

			caption := fmt.Sprintf("➤ %s:", update.Message.From.UserName)
			if update.Message.Caption != "" {
				caption = fmt.Sprintf("➤ \n**%s**: %s", update.Message.From.UserName, update.Message.Caption)
			}

			err = SendFileToDiscord(dg, relayChannelID, photoPath, caption)
			if err != nil {
				log.Printf("Failed to send photo to Discord: %v", err)
			}
			os.Remove(photoPath)
		}

		// Видеосообщения
		if update.Message.VideoNote != nil {
			videoFileID := update.Message.VideoNote.FileID
			fileURL, err := bot.GetFileDirectURL(videoFileID)
			if err != nil {
				log.Printf("Failed to get video URL: %v", err)
				continue
			}

			videoPath := fmt.Sprintf("content/video_%d.mp4", time.Now().UnixNano())
			if err := utils.DownloadFile(fileURL, videoPath); err != nil {
				log.Printf("Failed to download video: %v", err)
				continue
			}

			caption := fmt.Sprintf("➤ %s:", update.Message.From.UserName)
			err = SendFileToDiscord(dg, relayChannelID, videoPath, caption)
			if err != nil {
				log.Printf("Failed to send video to Discord: %v", err)
			}
			os.Remove(videoPath)
		}

		// Голосовые сообщения
		if update.Message.Voice != nil {
			voiceFileID := update.Message.Voice.FileID
			fileURL, err := bot.GetFileDirectURL(voiceFileID)
			if err != nil {
				log.Printf("Failed to get voice URL: %v", err)
				continue
			}

			voicePath := fmt.Sprintf("content/voice_%d.ogg", time.Now().UnixNano())
			if err := utils.DownloadFile(fileURL, voicePath); err != nil {
				log.Printf("Failed to download voice: %v", err)
				continue
			}

			caption := fmt.Sprintf("➤ %s:", update.Message.From.UserName)
			err = SendFileToDiscord(dg, relayChannelID, voicePath, caption)
			if err != nil {
				log.Printf("Failed to send voice to Discord: %v", err)
			}
			os.Remove(voicePath)
		}

		// Документы
		if update.Message.Document != nil {
			docFileID := update.Message.Document.FileID
			fileURL, err := bot.GetFileDirectURL(docFileID)
			if err != nil {
				log.Printf("Failed to get document URL: %v", err)
				continue
			}

			docPath := fmt.Sprintf("content/doc_%d_%s", time.Now().UnixNano(), update.Message.Document.FileName)
			if err := utils.DownloadFile(fileURL, docPath); err != nil {
				log.Printf("Failed to download document: %v", err)
				continue
			}

			caption := fmt.Sprintf("➤ %s:", update.Message.From.UserName)
			if update.Message.Caption != "" {
				caption = fmt.Sprintf("➤ \n**%s**: %s", update.Message.From.UserName, update.Message.Caption)
			}

			err = SendFileToDiscord(dg, relayChannelID, docPath, caption)
			if err != nil {
				log.Printf("Failed to send document to Discord: %v", err)
			}
			os.Remove(docPath)
		}
	}
}

func handleCommands(s *discordgo.Session, m *discordgo.MessageCreate, rank *ranking.Ranking) {
	command := strings.ToLower(m.Content)
	switch {
	case strings.HasPrefix(command, "!cpoll"):
		rank.HandlePollCommand(s, m, m.Content)
	case strings.HasPrefix(command, "!dep"):
		rank.HandleDepCommand(s, m, m.Content)
	case strings.HasPrefix(command, "!closedep"):
		rank.HandleCloseDepCommand(s, m, m.Content)
	case strings.HasPrefix(command, "!china give"):
		rank.HandleChinaGive(s, m, m.Content)
	case strings.HasPrefix(command, "!china rating"):
		rank.HandleChinaRating(s, m, m.Content)
	case strings.HasPrefix(command, "!admin give"):
		rank.HandleAdminGive(s, m, m.Content)
	case strings.HasPrefix(command, "!chelp"):
		rank.HandleHelpCommand(s, m)
	case command == "!top5":
		topUsers := rank.GetTop5()
		if len(topUsers) == 0 {
			s.ChannelMessageSend(m.ChannelID, "🏆 Топ-5 товарищей пуст! Партия разочарована!")
			return
		}
		response := "🏆 **Топ-5 товарищей по социальному рейтингу:**\n"
		for i, user := range topUsers {
			response += fmt.Sprintf("%d. <@%s> - %d социальных кредитов\n", i+1, user.ID, user.Rating)
		}
		s.ChannelMessageSend(m.ChannelID, response)
	}
}
