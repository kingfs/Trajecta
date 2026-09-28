//go:build ignore

package main

import (
	"context"
	"log"
	"os"

	entmigrate "github.com/kingfs/Trajecta/ent/migrate"
)

func main() {
	if err := entmigrate.Run(context.Background(), os.Args[1:]); err != nil {
		log.Printf("migration generation failed: %v", err)
		os.Exit(1)
	}
}
