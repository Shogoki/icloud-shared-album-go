package main

import (
	"fmt"
	"log"
	"os"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("Usage: go run main.go <token>")
	}

	token := os.Args[1]
	client := icloudalbum.NewClient()

	response, err := client.GetImages(token)
	if err != nil {
		log.Fatalf("Error getting images: %v", err)
	}

	fmt.Printf("Album: %s by %s %s\n",
		response.Metadata.StreamName,
		response.Metadata.UserFirstName,
		response.Metadata.UserLastName,
	)
	fmt.Printf("Total photos: %d\n\n", response.Metadata.ItemsReturned)

	// Print each photo's details
	for _, photo := range response.Photos {
		fmt.Printf("Photo: %s\n", photo.PhotoGUID)
		if photo.Caption != "" {
			fmt.Printf("Caption: %s\n", photo.Caption)
		}
		fmt.Printf("By: %s\n", photo.ContributorFullName)
		fmt.Printf("Created: %s\n", photo.DateCreated.Format("2006-01-02 15:04:05"))
		fmt.Printf("Size: %dx%d\n", photo.Width, photo.Height)
		fmt.Println("Derivatives:")
		for size, derivative := range photo.Derivatives {
			fmt.Printf("  %s: %dx%d", size, derivative.Width, derivative.Height)
			if derivative.URL != nil {
				fmt.Printf(" - %s", *derivative.URL)
			}
			fmt.Println()
		}
		fmt.Println()
	}
}
