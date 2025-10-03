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

// Start sets up the Discord and Telegram bots and starts the relay system.
func Start(discordToken, telegramToken, telegramChatID, floodChannelID, relayChannelID string, rank *ranking.Ranking) {
	dg := SetupDiscord(discordToken, floodChannelID, relayChannelID, rank)
	defer func() {
		rank.Stop() // Останавливаем горутину сброса
		dg.Close()
	}()

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
				msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("\n*%s*: %s", escapedUsername, escapedContent))
				msg.ParseMode = "MarkdownV2"
				if _, err := tgBot.Send(msg); err != nil {
					log.Printf("Failed to send message to Telegram: %v", err)
				}
			}

			// Вложения
			if len(m.Attachments) > 0 {
				for _, attachment := range m.Attachments {
					caption := fmt.Sprintf("\n%s:", m.Author.Username)
					if m.Content != "" {
						caption = fmt.Sprintf("\n%s: %s", m.Author.Username, m.Content)
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

	// Обработчик взаимодействий (кнопок)
	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Member.User.ID == s.State.User.ID {
			return
		}
		if i.Type == discordgo.InteractionMessageComponent {
			customID := i.MessageComponentData().CustomID
			log.Printf("Interaction received, CustomID: %s, ChannelID: %s, UserID: %s", customID, i.ChannelID, i.Member.User.ID)
			switch {
			case strings.HasPrefix(customID, "sell_confirm_"):
				log.Printf("Matched sell_confirm_")
				rank.HandleSellConfirm(s, i)
			case strings.HasPrefix(customID, "sell_cancel_"):
				log.Printf("Matched sell_cancel_")
				rank.HandleSellCancel(s, i)
			case strings.HasPrefix(customID, "sell_duplicates_confirm_"):
				log.Printf("Matched sell_duplicates_confirm_")
				rank.HandleSellDuplicatesConfirm(s, i)
			case strings.HasPrefix(customID, "sell_duplicates_cancel_"):
				log.Printf("Matched sell_duplicates_cancel_")
				rank.HandleSellDuplicatesCancel(s, i)
			case strings.HasPrefix(customID, "user_confirm_") || strings.HasPrefix(customID, "user_decline_") ||
				strings.HasPrefix(customID, "admin_accept_") || strings.HasPrefix(customID, "admin_reject_"):
				log.Printf("Matched cinema button: %s", customID)
				rank.HandleCinemaButton(s, i)
			case strings.HasPrefix(customID, "cinema_confirm_") || strings.HasPrefix(customID, "cinema_decline_"):
				log.Printf("Matched cinema button: %s", customID)
				rank.HandleCinemaButton(s, i)
			case strings.HasPrefix(customID, "blackjack_hit_"):
				log.Printf("Matched blackjack_hit_")
				rank.HandleBlackjackHit(s, i)
			case strings.HasPrefix(customID, "blackjack_stand_"):
				log.Printf("Matched blackjack_stand_")
				rank.HandleBlackjackStand(s, i)
			case strings.HasPrefix(customID, "blackjack_replay_"):
				log.Printf("Matched blackjack_replay_")
				rank.HandleBlackjackReplay(s, i)
			case strings.HasPrefix(customID, "rb_replay_"):
				log.Printf("Matched rb_replay_, calling HandleRBReplay")
				rank.HandleRBReplay(s, i)
			case strings.HasPrefix(customID, "duel_accept_"):
				log.Printf("Matched duel_accept_")
				rank.HandleDuelAccept(s, i)
			default:
				log.Printf("No match for CustomID: %s", customID)
			}
		} else {
			log.Printf("Received non-component interaction: %v", i.Type)
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
	command := strings.TrimSpace(strings.ToLower(m.Content))
	log.Printf("Processing command: %s from %s", command, m.Author.ID)
	switch {
	case strings.HasPrefix(command, "!cpoll"):
		log.Printf("Matched !cpoll")
		rank.HandlePollCommand(s, m, m.Content)
	case strings.HasPrefix(command, "!dep"):
		log.Printf("Matched !dep")
		rank.HandleDepCommand(s, m, m.Content)
	case strings.HasPrefix(command, "!closedep"):
		log.Printf("Matched !closedep")
		rank.HandleCloseDepCommand(s, m, m.Content)
	case command == "!top5" || command == "!top":
		log.Printf("Matched !top")
		rank.HandleTopCommand(s, m)
	case command == "!polls":
		log.Printf("Matched !polls")
		rank.HandlePollsCommand(s, m)
	case command == "!rb":
		log.Printf("Matched !rb, calling StartRBGame")
		rank.StartRBGame(s, m)
	case strings.HasPrefix(command, "!rb "):
		log.Printf("Matched !rb with arguments, calling HandleRBCommand")
		rank.HandleRBCommand(s, m, m.Content)
	case command == "!blackjack":
		log.Printf("Matched !blackjack")
		rank.StartBlackjackGame(s, m)
	case strings.HasPrefix(command, "!blackjack "):
		log.Printf("Matched !blackjack with arguments")
		rank.HandleBlackjackBet(s, m, m.Content)
	case strings.HasPrefix(command, "!endblackjack"):
		log.Printf("Matched !endblackjack")
		rank.HandleEndBlackjackCommand(s, m, m.Content)
	case strings.HasPrefix(command, "!duel"):
		log.Printf("Matched !duel")
		rank.HandleDuelCommand(s, m, m.Content)
	case strings.HasPrefix(command, "!stats"):
		log.Printf("Matched !stats")
		rank.HandleStatsCommand(s, m)
	case strings.HasPrefix(command, "!adminmass"):
		log.Printf("Matched !adminmass")
		rank.HandleAdminMassCommand(s, m, m.Content)
	case command == "!admincinemalist":
		log.Printf("Matched !admincinemalist")
		rank.HandleAdminCinemaListCommand(s, m)
	case strings.HasPrefix(command, "!removelowest "):
		log.Printf("Matched !removelowest")
		rank.HandleRemoveLowestCommand(s, m, command)
	case strings.HasPrefix(command, "!adjustcinema "):
		log.Printf("Matched !adjustcinema")
		rank.HandleAdjustCinemaCommand(s, m, command)
	case strings.HasPrefix(command, "!cinema "):
		log.Printf("Matched !cinema")
		rank.HandleCinemaCommand(s, m, command)
	case strings.HasPrefix(command, "!betcinema "):
		log.Printf("Matched !betcinema")
		rank.HandleBetCinemaCommand(s, m, command)
	case command == "!cinemalist":
		log.Printf("Matched !cinemalist")
		rank.HandleCinemaListCommand(s, m)
	case strings.HasPrefix(command, "!admin"):
		log.Printf("Matched !admin")
		rank.HandleAdminCommand(s, m, m.Content)
	case command == "!chelp":
		log.Printf("Matched !chelp")
		rank.HandleChelpCommand(s, m)
	case command == "!china":
		log.Printf("Matched !china")
		rank.HandleChinaCommand(s, m)
	case strings.HasPrefix(command, "!transfer"):
		log.Printf("Matched !transfer")
		rank.HandleTransferCommand(s, m, m.Content)
	case strings.HasPrefix(command, "!removecinema "):
		log.Printf("Matched !removecinema")
		rank.HandleRemoveCinemaCommand(s, m, command)
	case strings.HasPrefix(command, "!sync_nfts"):
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !sync_nfts")
		err := rank.Kki.SyncFromSheets(rank)
		if err != nil {
			s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка синхронизации**: "+err.Error())
		} else {
			s.ChannelMessageSend(m.ChannelID, "✅ **NFT и кейсы синхронизированы из Google Sheets!**")
		}
	case command == "!inventory":
		log.Printf("Matched !inventory")
		rank.HandleInventoryCommand(s, m)
	case command == "!sell_duplicates":
		log.Printf("Matched !sell_duplicates")
		rank.HandleSellDuplicatesCommand(s, m)
	case command == "!top_inventories":
		log.Printf("Matched !top_inventories")
		rank.HandleTopInventoriesCommand(s, m)
	case command == "!case_inventory":
		log.Printf("Matched !case_inventory")
		rank.HandleCaseInventoryCommand(s, m)
	case strings.HasPrefix(command, "!sell "):
		log.Printf("Matched !sell")
		rank.HandleSellCommand(s, m, command)
	case strings.HasPrefix(command, "!trade_nft "):
		log.Printf("Matched !trade_nft")
		rank.HandleTradeNFTCommand(s, m, command)
	case strings.HasPrefix(command, "!open_case "):
		log.Printf("Matched !open_case")
		rank.HandleOpenCaseCommand(s, m, command)
	case command == "!daily_case":
		log.Printf("Matched !daily_case")
		rank.HandleDailyCaseCommand(s, m)
	case strings.HasPrefix(command, "!case_trade "):
		log.Printf("Matched !case_trade")
		rank.HandleCaseTradeCommand(s, m, command)
	case strings.HasPrefix(command, "!a_give_case "):
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !a_give_case")
		rank.HandleAdminGiveCase(s, m, command)
	case strings.HasPrefix(command, "!a_give_nft "):
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !a_give_nft")
		rank.HandleAdminGiveNFT(s, m, command)
	case strings.HasPrefix(command, "!a_remove_nft "):
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !a_remove_nft")
		rank.HandleAdminRemoveNFT(s, m, command)
	case strings.HasPrefix(command, "!a_holiday_case "):
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !a_holiday_case")
		rank.HandleAdminHolidayCase(s, m, command)
	case strings.HasPrefix(command, "!a_give_holiday_case_all "):
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !a_give_holiday_case_all")
		rank.HandleAdminGiveHolidayCaseAll(s, m, command)
	case command == "!case_help":
		log.Printf("Matched !case_help")
		rank.HandleCaseHelpCommand(s, m)
	case strings.HasPrefix(command, "!show_nft "):
		log.Printf("Matched !show_nft")
		rank.HandleShowNFTCommand(s, m, command)
	case strings.HasPrefix(command, "!nft_show "):
		log.Printf("Matched !nft_show")
		rank.HandleShowNFTCommand(s, m, command)
	case command == "!test_clear_all_nfts":
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !test_clear_all_nfts")
		rank.ClearAllUserNFTs(s, m)
	case command == "!case_bank":
		log.Printf("Matched !case_bank")
		rank.HandleCaseBankCommand(s, m)
	case strings.HasPrefix(command, "!buy_case_bank "):
		log.Printf("Matched !buy_case_bank")
		rank.HandleBuyCaseBankCommand(s, m, command)
	case command == "!a_reset_case_limits":
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !a_reset_case_limits")
		rank.HandleResetCaseLimitsCommand(s, m)
	case command == "!btc":
		log.Printf("Matched !btc")
		rank.HandleBitcoinPriceCommand(s, m)
	case command == "!prices":
		log.Printf("Matched !prices")
		rank.HandlePriceStatsCommand(s, m)
	case command == "!a_refresh_bank":
		if !rank.IsAdmin(m.Author.ID) {
			return
		}
		log.Printf("Matched !a_refresh_bank")
		rank.HandleAdminRefreshBankCommand(s, m)
	default:
		log.Printf("No match for command: %s", command)
	}
}