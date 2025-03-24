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
			handleCommands(s, m, rank)
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
			s.ChannelMessageSend(m.ChannelID, "Демография владельцев Социальных Кредитов пока пуста.")
			return
		}
		response := "Топ-5 жителей Китая:\n"
		for i, user := range topUsers {
			response += fmt.Sprintf("%d. <@%s> - %d очков\n", i+1, user.ID, user.Rating)
		}
		s.ChannelMessageSend(m.ChannelID, response)
		return
	}

	if strings.HasPrefix(m.Content, "!rating") {
		parts := strings.Fields(m.Content)
		if len(parts) < 2 {
			s.ChannelMessageSend(m.ChannelID, "❌ Глупый Китайский житель! Вводи данные из привелегии правильно! Пример: !rating @username")
			return
		}
		userID := strings.TrimPrefix(parts[1], "<@")
		userID = strings.TrimSuffix(userID, ">")
		userID = strings.TrimPrefix(userID, "!")
		rating := rank.GetRating(userID)
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Социальные кредиты жителя Китая <@%s>: %d баллов", userID, rating))
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
		_, err = dg.ChannelMessageSend(channelID, caption)
		if err != nil {
			return fmt.Errorf("Failed to send message to Discord: %v", err)
		}
	}

	_, err = dg.ChannelFileSend(channelID, filePath, file)
	if err != nil {
		return fmt.Errorf("Failed to send file to Discord: %v", err)
	}
	return nil
}
