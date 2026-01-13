package main

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// loggingMiddleware wraps handlers to add request logging
func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next(rw, r)
		duration := time.Since(start)
		slog.Info("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.statusCode,
			"duration", duration,
			"remote_addr", r.RemoteAddr,
		)
	}
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// RSS Feed structures
type RSS struct {
	Channel Channel `xml:"channel"`
}

type Channel struct {
	Items []Item `xml:"item"`
}

type Item struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

// Article represents a Hacker News article
type Article struct {
	ID          int
	Date        string
	ArticleLink string
	CommentLink string
	Title       string
	CreatedAt   time.Time
	Read        bool
}

// TemplateData holds data to pass to templates
type TemplateData struct {
	Title        string
	LastSyncTime time.Time
	Articles     []Article
}

// Database global
var db *sql.DB

// Last sync time with mutex for thread safety
var (
	lastSyncTime time.Time
	syncTimeMu   sync.RWMutex
)

// Templates holds parsed templates
var templates *template.Template

// HTTP client with timeout
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// initDB initializes the SQLite database
func initDB() error {
	// Create db directory if it doesn't exist
	if err := os.MkdirAll("db", 0755); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}

	var err error
	db, err = sql.Open("sqlite3", "./db/hn_reader.db")
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool limits for thread safety
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Create articles table
	createTableSQL := `CREATE TABLE IF NOT EXISTS articles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT NOT NULL,
		article_link TEXT NOT NULL,
		comment_link TEXT NOT NULL,
		title TEXT NOT NULL,
		read INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(article_link, comment_link)
	);`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	slog.Info("Database initialized successfully")
	return nil
}

// loadTemplates loads all HTML templates
func loadTemplates() error {
	var err error
	templates, err = template.ParseGlob(filepath.Join("templates", "*.html"))
	if err != nil {
		return fmt.Errorf("failed to load templates: %w", err)
	}
	slog.Info("Templates loaded successfully")
	return nil
}

// fetchAndParseRSS fetches the RSS feed and parses it
func fetchAndParseRSS() (*RSS, error) {
	resp, err := httpClient.Get("https://www.daemonology.net/hn-daily/index.rss")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch RSS: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read RSS body: %w", err)
	}

	var rss RSS
	err = xml.Unmarshal(body, &rss)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RSS: %w", err)
	}

	slog.Info("Successfully fetched RSS feed", "items", len(rss.Channel.Items))
	return &rss, nil
}

// parseArticlesFromDescription extracts article links from the CDATA description
func parseArticlesFromDescription(description, date string) []Article {
	var articles []Article

	// Split by <li> tags
	lines := strings.Split(description, "<li>")

	for _, line := range lines {
		if !strings.Contains(line, "storylink") {
			continue
		}

		var articleLink, commentLink, title string

		// Extract article link and title
		if idx := strings.Index(line, `<span class="storylink"><a href="`); idx != -1 {
			start := idx + len(`<span class="storylink"><a href="`)
			end := strings.Index(line[start:], `"`)
			if end != -1 {
				articleLink = line[start : start+end]
			}

			// Extract title
			titleStart := strings.Index(line[start+end:], `">`) + start + end + 2
			titleEnd := strings.Index(line[titleStart:], "</a>")
			if titleEnd != -1 {
				title = line[titleStart : titleStart+titleEnd]
			}
		}

		// Extract comment link
		if idx := strings.Index(line, `<span class="postlink"><a href="`); idx != -1 {
			start := idx + len(`<span class="postlink"><a href="`)
			end := strings.Index(line[start:], `"`)
			if end != -1 {
				commentLink = line[start : start+end]
			}
		}

		if articleLink != "" && commentLink != "" && title != "" {
			articles = append(articles, Article{
				Date:        date,
				ArticleLink: articleLink,
				CommentLink: commentLink,
				Title:       title,
			})
		}
	}

	return articles
}

// saveArticle saves an article to the database and returns whether it was inserted
func saveArticle(article Article) (bool, error) {
	result, err := db.Exec(`
		INSERT OR IGNORE INTO articles (date, article_link, comment_link, title)
		VALUES (?, ?, ?, ?)
	`, article.Date, article.ArticleLink, article.CommentLink, article.Title)

	if err != nil {
		return false, fmt.Errorf("failed to save article: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

// processFeed fetches and processes the RSS feed
func processFeed() {
	slog.Info("Starting RSS feed processing")

	rss, err := fetchAndParseRSS()
	if err != nil {
		slog.Error("Error fetching RSS", "error", err)
		return
	}

	newArticles := 0
	for _, item := range rss.Channel.Items {
		articles := parseArticlesFromDescription(item.Description, item.PubDate)

		for _, article := range articles {
			inserted, err := saveArticle(article)
			if err != nil {
				slog.Error("Error saving article", "error", err, "title", article.Title)
			} else if inserted {
				newArticles++
			}
		}
	}

	syncTimeMu.Lock()
	lastSyncTime = time.Now()
	syncTimeMu.Unlock()

	slog.Info("Feed processing complete", "new_articles", newArticles)
}

// getUnreadCount returns the count of unread articles
func getUnreadCount() (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM articles WHERE read = 0`).Scan(&count)
	return count, err
}

// getAllArticles retrieves all unread articles from the database
func getAllArticles() ([]Article, error) {
	rows, err := db.Query(`
		SELECT id, date, article_link, comment_link, title, read, created_at
		FROM articles
		WHERE read = 0
		ORDER BY created_at ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var a Article
		var readInt int
		err := rows.Scan(&a.ID, &a.Date, &a.ArticleLink, &a.CommentLink, &a.Title, &readInt, &a.CreatedAt)
		if err != nil {
			return nil, err
		}
		a.Read = readInt == 1
		articles = append(articles, a)
	}

	return articles, nil
}

// markArticleRead marks an article as read or unread
func markArticleRead(id int, read bool) error {
	readInt := 0
	if read {
		readInt = 1
	}
	_, err := db.Exec(`UPDATE articles SET read = ? WHERE id = ?`, readInt, id)
	return err
}

// Handler functions
func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	articles, err := getAllArticles()
	if err != nil {
		slog.Error("Error fetching articles", "error", err)
		articles = []Article{}
	}

	syncTimeMu.RLock()
	syncTime := lastSyncTime
	syncTimeMu.RUnlock()

	data := TemplateData{
		Title:        "HN Reader",
		LastSyncTime: syncTime,
		Articles:     articles,
	}

	if err := templates.ExecuteTemplate(w, "home.html", data); err != nil {
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
		slog.Error("Template error", "error", err)
	}
}

func syncHandler(w http.ResponseWriter, r *http.Request) {
	// Run the feed processing asynchronously
	go processFeed()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status": "sync started", "timestamp": "%s"}`, time.Now().Format(time.RFC3339))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status": "healthy", "timestamp": "%s"}`, time.Now().Format(time.RFC3339))
}

func apiDataHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	data := `{
	"message": "Hello from the API",
	"timestamp": "%s",
	"method": "%s"
}`
	fmt.Fprintf(w, data, time.Now().Format(time.RFC3339), r.Method)
}

func markReadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := r.URL.Query().Get("id")
	readStr := r.URL.Query().Get("read")

	if idStr == "" || readStr == "" {
		http.Error(w, "Missing id or read parameter", http.StatusBadRequest)
		return
	}

	id := 0
	fmt.Sscanf(idStr, "%d", &id)
	read := readStr == "true"

	err := markArticleRead(id, read)
	if err != nil {
		http.Error(w, "Failed to update article", http.StatusInternalServerError)
		slog.Error("Error updating article", "error", err, "id", id)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status": "success"}`)
}

func main() {
	// Initialize structured logger
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	slog.Info("Starting web server")

	// Initialize database
	if err := initDB(); err != nil {
		slog.Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Load templates
	if err := loadTemplates(); err != nil {
		slog.Error("Failed to load templates", "error", err)
		os.Exit(1)
	}

	// Serve static files (favicons, etc.)
	fileServer := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fileServer))

	// Register routes with logging middleware
	http.HandleFunc("/", loggingMiddleware(homeHandler))
	http.HandleFunc("/sync", loggingMiddleware(syncHandler))
	http.HandleFunc("/mark-read", loggingMiddleware(markReadHandler))
	http.HandleFunc("/health", loggingMiddleware(healthHandler))
	http.HandleFunc("/api/data", loggingMiddleware(apiDataHandler))

	// Server configuration
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := fmt.Sprintf(":%s", port)

	// Create HTTP server
	server := &http.Server{
		Addr:         addr,
		Handler:      nil,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start automatic refresh ticker (every 2 hours)
	ticker := time.NewTicker(2 * time.Hour)
	defer ticker.Stop()

	go func() {
		for range ticker.C {
			slog.Info("Automatic feed refresh triggered")
			processFeed()
		}
	}()

	// Setup graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-shutdown
		slog.Info("Shutdown signal received", "signal", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			slog.Error("Server shutdown error", "error", err)
			os.Exit(1)
		}
	}()

	slog.Info("Server listening", "address", "http://localhost"+addr)
	slog.Info("Automatic feed refresh enabled", "interval", "2 hours")

	// Start server
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Server failed to start", "error", err)
		os.Exit(1)
	}

	slog.Info("Server stopped gracefully")
}
