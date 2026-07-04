package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/cozy-creator/runpod-go-sdk"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("failed to load .env")
	}

	apiKey := os.Getenv("RUNPOD_API_KEY")
	endpointID := os.Getenv("RUNPOD_ENDPOINT_ID")

	client, err := runpod.NewClient(apiKey,
		runpod.WithDebug(true),
		runpod.WithTimeout(60*time.Second),
	)
	if err != nil {
		log.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// Step 1: Submit a few jobs
	inputs := []interface{}{
		map[string]interface{}{"prompt": "Job 1 - Just wait"},
		map[string]interface{}{"prompt": "Job 2 - Please wait"},
		map[string]interface{}{"prompt": "Job 3 - This should be purged"},
	}

	fmt.Println("submitting jobs...")
	for i, input := range inputs {
		job, err := client.RunAsync(ctx, endpointID, input)
		if err != nil {
			log.Fatalf("failed to submit job %d: %v", i+1, err)
		}
		fmt.Printf("job %d submitted: %s\n", i+1, job.ID)
	}

	// Step 2: Immediately purge the queue
	fmt.Println("purging queued jobs...")
	if err := client.PurgeQueue(ctx, endpointID); err != nil {
		log.Fatalf("failed to purge queue: %v", err)
	}
	fmt.Println("purge request sent")
}
