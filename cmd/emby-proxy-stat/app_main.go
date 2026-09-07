package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	flag.Parse()
	if *passwordHashFlag {
		password, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			log.Fatalf("failed to read password: %v", err)
		}
		hash, err := generatePasswordHash(strings.TrimSuffix(string(password), "\n"))
		if err != nil {
			log.Fatalf("failed to generate password hash: %v", err)
		}
		fmt.Println(hash)
		return
	}
	if err := reloadConfig(); err != nil {
		log.Fatalf("[Fatal] Failed to load config: %v", err)
	}
	if err := initDB(); err != nil {
		log.Fatalf("[Fatal] Failed to init DB: %v", err)
	}
	defer db.Close()

	go logTailWorker()
	go flushTrafficWorker()
	go telegramSchedulerWorker()
	startProxyGuard()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/stats", handleStats)
	mux.HandleFunc("/api/test-tg", handleTestTG)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "engine": "go"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(indexHTML)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not Found"})
	})

	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", *portFlag),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	log.Printf("[Go Stats Server] Running on 127.0.0.1:%d", *portFlag)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[Fatal] Server error: %v", err)
	}
}
