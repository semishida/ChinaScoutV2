package bot

import (
	"fmt"
	"log"
	"os"
	"strings"

	"csv2/ranking"
	"github.com/bwmarrin/discordgo"
)

func setupDiscord(token, floodChannelID, relayChannelID string, rank *ranking.Ranking) *discordgo.Session {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Failed to initialize Discord bot: %v", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentMessageContent | discordgo.IntentsGuildVoiceStates
	rank.TrackVoiceActivity(dg)

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID {
			return
		}

		if m.ChannelID == floodChannelID && strings.HasPrefix(m.Content, "!") {
			log.Printf("Received command: %s from %s in flood channel", m.Content, m.Author.Username)
			if strings.HasPrefix(m.Content, "!dep") {
				rank.HandleDepCommand(s, m, m.Content)
			} else if strings.HasPrefix(m.Content, "!china adm") {
				rank.HandleChinaCommandAdmin(s, m, m.Content)
			} else {
				handleCommands(s, m, rank)
			}
			return
		}

		if m.ChannelID == relayChannelID {
			log.Printf("Received message for relay: %s from %s", m.Content, m.Author.Username)
		}
	})

	if err := dg.Open(); err != nil {
		log.Fatalf("Failed to open Discord session: %v", err)
	}
	log.Println("Discord bot is running.")
	return dg
}

func handleCommands(s *discordgo.Session, m *discordgo.MessageCreate, rank *ranking.Ranking) {
	if strings.HasPrefix(m.Content, "!china") {
		rank.HandleChinaCommand(s, m, m.Content)
		return
	}

	if m.Content == "!top5" {
		topUsers := rank.GetTop5()
		if len(topUsers) == 0 {
			if _, err := s.ChannelMessageSend(m.ChannelID, "Демография владельцев Социальных Кредитов пока пуста."); err != nil {
				log.Printf("Failed to send top5 empty response: %v", err)
			}
			return
		}
		response := "Топ-5 жителей Китая:\n"
		for i, user := range topUsers {
			response += fmt.Sprintf("%d. <@%s> - %d очков\n", i+1, user.ID, user.Rating)
		}
		if _, err := s.ChannelMessageSend(m.ChannelID, response); err != nil {
			log.Printf("Failed to send top5 response: %v", err)
		}
		log.Printf("Sent top5 response for %s", m.Author.ID)
		return
	}

	if strings.HasPrefix(m.Content, "!rating") {
		parts := strings.Fields(m.Content)
		if len(parts) < 2 {
			if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Глупый Китайский житель! Вводи данные из привилегии правильно! Пример: !rating @username"); err != nil {
				log.Printf("Failed to send rating usage response: %v", err)
			}
			return
		}
		userID := strings.TrimPrefix(parts[1], "<@")
		userID = strings.TrimSuffix(userID, ">")
		userID = strings.TrimPrefix(userID, "!")
		rating := rank.GetRating(userID)
		if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Социальные кредиты жителя Китая <@%s>: %d баллов", userID, rating)); err != nil {
			log.Printf("Failed to send rating response for %s: %v", userID, err)
		}
		log.Printf("Sent rating response for %s: %d", userID, rating)
		return
	}

	if m.Content == "!help" {
		response := "📜 **Список команд бота:**\n" +
			"**!china @id +X [причина]** - Передать X кредитов пользователю со своего баланса.\n" +
			"**!china adm @id +X [причина]** - (Админ) Выдать X кредитов пользователю.\n" +
			"**!dep poll \"Тема\" \"Вариант1\" \"Вариант2\"** - (Админ) Создать опрос для ставок.\n" +
			"**!dep <сумма> <вариант>** - Сделать ставку на вариант опроса.\n" +
			"**!dep depres \"вариант\"** - (Админ) Завершить опрос и распределить выигрыш.\n" +
			"**!top5** - Показать топ-5 пользователей по кредитам.\n" +
			"**!rating @id** - Узнать баланс пользователя.\n" +
			"**!help** - Показать это сообщение."
		if _, err := s.ChannelMessageSend(m.ChannelID, response); err != nil {
			log.Printf("Failed to send help response: %v", err)
		}
		log.Printf("Sent help response for %s", m.Author.ID)
		return
	}
}

func SendFileToDiscord(dg *discordgo.Session, channelID, filePath, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("Failed to open file: %v", err)
	}
	defer file.Close()

	if caption != "" {
		if _, err := dg.ChannelMessageSend(channelID, caption); err != nil {
			log.Printf("Failed to send caption to Discord: %v", err)
			return fmt.Errorf("Failed to send message to Discord: %v", err)
		}
	}

	_, err = dg.ChannelFileSend(channelID, filePath, file)
	if err != nil {
		log.Printf("Failed to send file to Discord: %v", err)
		return fmt.Errorf("Failed to send file to Discord: %v", err)
	}
	log.Printf("Sent file to Discord channel %s: %s", channelID, filePath)
	return nil
}
