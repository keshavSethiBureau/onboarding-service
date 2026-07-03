// Command server is the entrypoint for the onboarding service.
package main

import (
	"log"

	"onboarding-service/internal/app"
)

func main() {
	container, err := app.Wire()
	if err != nil {
		log.Fatalf("failed to wire application: %v", err)
	}
	if err := app.Run(container); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
