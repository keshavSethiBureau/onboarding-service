// Command server is the entrypoint for the onboarding service.
package main

import (
	"log"
	"os"

	"github.com/bureau/onboarding-service/internal/app"
)

func main() {
	// Port comes from the PORT env var with a sane default. Boot/infra config
	// will move to commons configloader later (see onboarding-lld.md §9); kept
	// stdlib-only for now.
	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}

	if err := app.New().Run(addr); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
