package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := getEnv("ADDR", ":8080")
	fmt.Printf("Server running at http://localhost%s\n", addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           newRouter(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}
