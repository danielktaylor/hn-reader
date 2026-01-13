package main

import (
	"database/sql"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Logger wraps the standard logger with additional context
type Logger struct {
	*log.Logger
}

// NewLogger creates a new logger instance
func NewLogger() *Logger {
	return &Logger{
		Logger: log.New(os.Stdout, "", log.LstdFlags),
	}
}

// LogRequest logs HTTP request details
func (l *Logger) LogRequest(r *http.Request, status int, duration time.Duration) {
	l.Printf("[%s] %s %s - Status: %d - Duration: %v - RemoteAddr: %s",
		r.Method,
		r.URL.Path,
		r.Proto,
		status,
		duration,
		r.RemoteAddr,
	)
}

// loggingMiddleware wraps handlers to add request logging
func loggingMiddleware(logger *Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next(rw, r)
		duration := time.Since(start)
		logger.LogRequest(r, rw.statusCode, duration)
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
}

// TemplateData holds data to pass to templates
type TemplateData struct {
	Title       string
	CurrentTime string
	Articles    []Article
}

// Database global
var db *sql.DB

// Templates holds parsed templates
var templates *template.Template

// initDB initializes the SQLite database
func initDB(logger *Logger) error {
	var err error
	db, err = sql.Open("sqlite3", "./hn_reader.db")
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Create articles table
	createTableSQL := `CREATE TABLE IF NOT EXISTS articles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT NOT NULL,
		article_link TEXT NOT NULL,
		comment_link TEXT NOT NULL,
		title TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(article_link, comment_link)
	);`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	logger.Println("Database initialized successfully")
	return nil
}

// loadTemplates loads all HTML templates
func loadTemplates(logger *Logger) error {
	var err error
	templates, err = template.ParseGlob(filepath.Join("templates", "*.html"))
	if err != nil {
		return fmt.Errorf("failed to load templates: %w", err)
	}
	logger.Println("Templates loaded successfully")
	return nil
}

// fetchAndParseRSS fetches the RSS feed and parses it
func fetchAndParseRSS(logger *Logger) (*RSS, error) {
	resp, err := http.Get("https://www.daemonology.net/hn-daily/index.rss")
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

	logger.Printf("Successfully fetched RSS feed with %d items", len(rss.Channel.Items))
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

// saveArticle saves an article to the database
func saveArticle(article Article, logger *Logger) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO articles (date, article_link, comment_link, title)
		VALUES (?, ?, ?, ?)
	`, article.Date, article.ArticleLink, article.CommentLink, article.Title)

	if err != nil {
		return fmt.Errorf("failed to save article: %w", err)
	}

	return nil
}

// processFeed fetches and processes the RSS feed
func processFeed(logger *Logger) {
	logger.Println("Starting RSS feed processing...")

	rss, err := fetchAndParseRSS(logger)
	if err != nil {
		logger.Printf("Error fetching RSS: %v", err)
		return
	}

	newArticles := 0
	for _, item := range rss.Channel.Items {
		articles := parseArticlesFromDescription(item.Description, item.PubDate)

		for _, article := range articles {
			err := saveArticle(article, logger)
			if err != nil {
				logger.Printf("Error saving article: %v", err)
			} else {
				newArticles++
			}
		}
	}

	logger.Printf("Feed processing complete. Processed %d new articles", newArticles)
}

// getAllArticles retrieves all articles from the database
func getAllArticles() ([]Article, error) {
	rows, err := db.Query(`
		SELECT id, date, article_link, comment_link, title, created_at
		FROM articles
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var a Article
		err := rows.Scan(&a.ID, &a.Date, &a.ArticleLink, &a.CommentLink, &a.Title, &a.CreatedAt)
		if err != nil {
			return nil, err
		}
		articles = append(articles, a)
	}

	return articles, nil
}

// Handler functions
func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	articles, err := getAllArticles()
	if err != nil {
		log.Printf("Error fetching articles: %v", err)
		articles = []Article{}
	}

	data := TemplateData{
		Title:       "HN Reader",
		CurrentTime: time.Now().Format(time.RFC1123),
		Articles:    articles,
	}

	if err := templates.ExecuteTemplate(w, "home.html", data); err != nil {
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
		log.Printf("Template error: %v", err)
	}
}

func syncHandler(logger *Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Run the feed processing asynchronously
		go processFeed(logger)

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status": "sync started", "timestamp": "%s"}`, time.Now().Format(time.RFC3339))
	}
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

func main() {
	logger := NewLogger()
	logger.Println("Starting web server...")

	// Initialize database
	if err := initDB(logger); err != nil {
		logger.Fatal(err)
	}
	defer db.Close()

	// Load templates
	if err := loadTemplates(logger); err != nil {
		logger.Fatal(err)
	}

	// Register routes with logging middleware
	http.HandleFunc("/", loggingMiddleware(logger, homeHandler))
	http.HandleFunc("/sync", loggingMiddleware(logger, syncHandler(logger)))
	http.HandleFunc("/health", loggingMiddleware(logger, healthHandler))
	http.HandleFunc("/api/data", loggingMiddleware(logger, apiDataHandler))

	// Server configuration
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := fmt.Sprintf(":%s", port)
	logger.Printf("Server listening on http://localhost%s", addr)

	// Start server
	if err := http.ListenAndServe(addr, nil); err != nil {
		logger.Fatal("Server failed to start: ", err)
	}
}
