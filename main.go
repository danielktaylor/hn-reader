package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
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
		
		// Create a response writer wrapper to capture status code
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

// TemplateData holds data to pass to templates
type TemplateData struct {
	Title       string
	CurrentTime string
}

// Templates holds parsed templates
var templates *template.Template

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

// Handler functions
func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	
	data := TemplateData{
		Title:       "Go Web App",
		CurrentTime: time.Now().Format(time.RFC1123),
	}
	
	if err := templates.ExecuteTemplate(w, "home.html", data); err != nil {
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
		log.Printf("Template error: %v", err)
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
	
	// Load templates
	if err := loadTemplates(logger); err != nil {
		logger.Fatal(err)
	}
	
	// Register routes with logging middleware
	http.HandleFunc("/", loggingMiddleware(logger, homeHandler))
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