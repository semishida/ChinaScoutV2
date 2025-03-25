package bot

import (
	"fmt"
	"log"
	"os"

	"csv2/handlers"
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
		handlers.HandleMessageCreate(s, m, rank, floodChannelID, relayChannelID)
	})

	if err := dg.Open(); err != nil {
		log.Fatalf("Failed to open Discord session: %v", err)
	}
	log.Println("Discord bot is running.")
	return dg
}

func SendFileToDiscord(dg *discordgo.Session, channelID, filePath, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	if caption != "" {
		if _, err := dg.ChannelMessageSend(channelID, caption); err != nil {
			log.Printf("Failed to send caption to Discord: %v", err)
			return fmt.Errorf("failed to send message to Discord: %v", err)
		}
	}

	_, err = dg.ChannelFileSend(channelID, filePath, file)
	if err != nil {
		log.Printf("Failed to send file to Discord: %v", err)
		return fmt.Errorf("failed to send file to Discord: %v", err)
	}
	log.Printf("Sent file to Discord channel %s: %s", channelID, filePath)
	return nil
}
