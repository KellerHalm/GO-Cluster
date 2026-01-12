package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := flag.String("port", "8080", "Port to listen on")
	id := flag.String("id", "unknown", "Replica ID")
	flag.Parse()

	startedAt := time.Now()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Эмуляция полезной нагрузки
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintf(w, "Hello from Worker ID: %s. Uptime: %s\n", *id, time.Since(startedAt))
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	log.Printf("Worker %s started on port %s", *id, *port)
	if err := http.ListenAndServe(":"+*port, nil); err != nil {
		log.Printf("Worker failed: %v", err)
		os.Exit(1)
	}
}
