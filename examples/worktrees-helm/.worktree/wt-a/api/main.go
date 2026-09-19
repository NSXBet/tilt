package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

// A tiny API server for the worktrees-helm example. It reports the shared
// GREETING (from the main-scoped Secret) and the pod that served the
// request, so stable and clone deployments are distinguishable.
func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		host, _ := os.Hostname()
		fmt.Fprintf(w, "%s | served by pod %s | branch wt-a\n", os.Getenv("GREETING"), host)
	})

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

