package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"csv2/bot"
	"csv2/ranking"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}

	discordToken := os.Getenv("DISCORD_TOKEN")
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	telegramChatID := os.Getenv("TELEGRAM_CHAT_ID")
	floodChannelID := os.Getenv("DISCORD_FLOOD_CHANNEL_ID")
	relayChannelID := os.Getenv("DISCORD_RELAY_CHANNEL_ID")
	adminFilePath := os.Getenv("ADMIN_FILE_PATH")
	redisAddr := os.Getenv("REDIS_ADDR")

	if discordToken == "" || telegramToken == "" || telegramChatID == "" || floodChannelID == "" || relayChannelID == "" || adminFilePath == "" || redisAddr == "" {
		log.Fatal("Missing required environment variables")
	}

	rank, err := ranking.NewRanking(adminFilePath, redisAddr)
	if err != nil {
		log.Fatalf("Failed to initialize ranking: %v", err)
	}

	go rank.PeriodicSave()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		log.Printf("Received signal %s. Shutting down...", sig)
		os.Exit(0)
	}()

	bot.Start(discordToken, telegramToken, telegramChatID, floodChannelID, relayChannelID, rank)
}
