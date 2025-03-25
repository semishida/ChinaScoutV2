package bot

import (
	"fmt"
	"log"
	"strings"

	"csv2/ranking"
	"github.com/bwmarrin/discordgo"
)

func SetupDiscord(token, floodChannelID, relayChannelID string, rank *ranking.Ranking) *discordgo.Session {
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
			log.Printf("Received command: %s from %s in flood channel", m.Content, m.Author.ID)
			switch {
			case strings.HasPrefix(m.Content, "!poll #"):
				rank.HandlePollClose(s, m, m.Content)
			case strings.HasPrefix(m.Content, "!poll"):
				rank.HandlePollCommand(s, m, m.Content)
			case strings.HasPrefix(m.Content, "!dep"):
				rank.HandleDepCommand(s, m, m.Content)
			case strings.HasPrefix(m.Content, "!admin"):
				rank.HandleAdminCommand(s, m, m.Content)
			case strings.HasPrefix(m.Content, "!china give"):
				rank.HandleChinaGive(s, m, m.Content)
			case strings.HasPrefix(m.Content, "!china rating"):
				rank.HandleChinaRating(s, m, m.Content)
			case strings.HasPrefix(m.Content, "!china help"):
				rank.HandleChinaHelp(s, m, m.Content)
			case m.Content == "!top5":
				topUsers := rank.GetTop5()
				if len(topUsers) == 0 {
					s.ChannelMessageSend(m.ChannelID, "🏆 Топ-5 пуст!")
					return
				}
				response := "🏆 **Топ-5 игроков:**\n"
				for i, user := range topUsers {
					response += fmt.Sprintf("%d. <@%s> - %d кредитов\n", i+1, user.ID, user.Rating)
				}
				s.ChannelMessageSend(m.ChannelID, response)
			}
			return
		}

		if m.ChannelID == relayChannelID {
			log.Printf("Received message in relay: %s from %s", m.Content, m.Author.ID)
		}
	})

	if err := dg.Open(); err != nil {
		log.Fatalf("Failed to open Discord session: %v", err)
	}
	log.Println("Discord bot is running.")
	return dg
}
