package main

import (
	"flag"
	"log"

	"apigw/pkg/config"
	"apigw/pkg/server"
)

func main() {
	configPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	srv := server.NewServer(cfg)
	log.Printf("Starting API Gateway on port %d", cfg.Server.Port)
	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}
}
