package main

import (
	"log"
	"os"

	"csv2/bot"
	"csv2/ranking"
)

func main() {
	token := os.Getenv("DISCORD_TOKEN")
	floodChannelID := os.Getenv("FLOOD_CHANNEL_ID")
	relayChannelID := os.Getenv("RELAY_CHANNEL_ID")
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	adminFilePath := "admins.json"
	redisAddr := "localhost:6379"

	rank, err := ranking.NewRanking(adminFilePath, redisAddr)
	if err != nil {
		log.Fatalf("Failed to initialize ranking: %v", err)
	}

	discord := bot.SetupDiscord(token, floodChannelID, relayChannelID, rank)
	bot.SetupTelegram(telegramToken, floodChannelID, relayChannelID, discord, rank)

	select {}
}
