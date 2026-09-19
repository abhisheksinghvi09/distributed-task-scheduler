// Command apikey is the bootstrap tool for the auth layer: there is no
// self-service signup, so a key has to come from somewhere. Usage:
//
//	apikey -tenant <uuid> -name "ci pipeline"
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/abhisheksinghvi09/task-scheduler/internal/auth"
	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/db"
	"github.com/google/uuid"
)

func main() {
	tenant := flag.String("tenant", "", "Tenant UUID to issue the key for. Omit to mint a fresh tenant id -- tenants are not a separate stored entity, just a UUID convention shared by every row that carries one.")
	name := flag.String("name", "", "Human-readable label for the key.")
	flag.Parse()

	if *tenant == "" {
		*tenant = uuid.NewString()
		fmt.Printf("tenant:  %s (newly minted)\n", *tenant)
	}

	ctx := context.Background()
	pool, err := common.ConnectToDatabase(ctx, common.GetDBConnectionString())
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	rawKey, keyID, err := auth.Generate(ctx, pool, *tenant, *name)
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}

	fmt.Printf("key_id:  %s\n", keyID)
	fmt.Printf("api_key: %s\n", rawKey)
	fmt.Println("This key is shown once and is not recoverable -- store it now.")
}
