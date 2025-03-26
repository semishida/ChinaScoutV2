package main

import (
	"log"
	"os"

	"csv2/bot"
	"csv2/ranking"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Printf("Warning: Failed to load .env file: %v", err)
	}

	discordToken := os.Getenv("DISCORD_TOKEN")
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	floodChannelID := os.Getenv("FLOOD_CHANNEL_ID")
	relayChannelID := os.Getenv("RELAY_CHANNEL_ID")
	adminFilePath := os.Getenv("ADMIN_FILE_PATH")
	redisAddr := os.Getenv("REDIS_ADDR")
	telegramChatID := os.Getenv("TELEGRAM_CHAT_ID") // Добавляем ID чата Telegram

	if discordToken == "" {
		log.Fatal("DISCORD_TOKEN is not set")
	}
	if telegramToken == "" {
		log.Fatal("TELEGRAM_TOKEN is not set")
	}
	if telegramChatID == "" {
		log.Fatal("TELEGRAM_CHAT_ID is not set")
	}

	rank, err := ranking.NewRanking(adminFilePath, redisAddr)
	if err != nil {
		log.Fatalf("Failed to initialize ranking: %v", err)
	}

	bot.Start(discordToken, telegramToken, telegramChatID, floodChannelID, relayChannelID, rank)
}
