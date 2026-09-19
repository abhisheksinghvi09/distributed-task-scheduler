package main

import (
	"flag"
	"log"

	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/scheduler"
)

var schedulerPort = flag.String("scheduler_port", ":8081", "Port on which the Scheduler serves requests.")

func main() {
	flag.Parse()
	dbConnectString := common.GetDBConnectionString()
	server := scheduler.NewServer(*schedulerPort, dbConnectString)
	if err := server.Start(); err != nil {
		log.Fatalf("scheduler exited: %v", err)
	}
}
