package bot

import (
	"fmt"
	"log"
	"os"
	"time"

	"csv2/ranking"
	"github.com/bwmarrin/discordgo"
)

func SetupDiscord(token, floodChannelID, relayChannelID string, rank *ranking.Ranking) *discordgo.Session {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Failed to initialize Discord bot: %v", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentMessageContent | discordgo.IntentsGuildVoiceStates
	// Удаляем некорректный вызов
	// rank.TrackVoiceActivity(dg)

	for i := 0; i < 5; i++ {
		err = dg.Open()
		if err == nil {
			break
		}
		log.Printf("Failed to open Discord session (attempt %d/5): %v", i+1, err)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		log.Fatalf("Failed to open Discord session after 5 attempts: %v", err)
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
