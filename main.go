package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	sessionCookieName = "sesystem_session"
	defaultRollingKey = "SESYSTEM"
)

type App struct {
	db     *sql.DB
	tmpl   *template.Template
	secret string
}

type User struct {
	ID       int
	Username string
	Password string
	Role     string
}

type Client struct {
	ID      int
	Name    string
	Phone   string
	Address string
}

type Job struct {
	ID         int
	Title      string
	ClientID   int
	ClientName string
	DueDate    string
	Status     string
	Revenue    float64
	Expenses   float64
	Notes      string
	Profit     float64
}

type Session struct {
	ID        string
	ExpiresAt time.Time
	UserID    int
}

type Summary struct {
	TotalRevenue  float64
	TotalExpenses float64
	TotalProfit   float64
	Pending       int
	InProgress    int
	Completed     int
}

func main() {
	if err := os.MkdirAll("templates", 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll("static", 0o755); err != nil {
		log.Fatal(err)
	}

	logFile, err := os.OpenFile("sesystem.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))

	db, err := initDB("sesystem.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	tmpl := template.Must(template.ParseGlob("templates/*.html"))
	secret := os.Getenv("SESYSTEM_SECRET")
	if secret == "" {
		secret = defaultRollingKey
	}

	app := &App{db: db, tmpl: tmpl, secret: secret}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.withLogging(app.handleRoot))
	mux.HandleFunc("/jobs", app.withLogging(app.requireLocalhost(app.requireAuth(app.handleJobs))))
	mux.HandleFunc("/clients", app.withLogging(app.requireLocalhost(app.requireAuth(app.handleClients))))
	mux.HandleFunc("/receipts", app.withLogging(app.requireLocalhost(app.requireAuth(app.handleReceipts))))
	mux.HandleFunc("/spreadsheets", app.withLogging(app.requireLocalhost(app.requireAuth(app.handleSpreadsheets))))
	mux.HandleFunc("/finances", app.withLogging(app.requireLocalhost(app.requireAuth(app.requireAdmin(app.handleFinances)))))
	mux.HandleFunc("/users", app.withLogging(app.requireLocalhost(app.requireAuth(app.requireAdmin(app.handleUsers)))))
	mux.HandleFunc("/receipt/", app.withLogging(app.handlePublicReceipt))
	mux.HandleFunc("/logout", app.withLogging(app.requireLocalhost(app.handleLogout)))
	mux.HandleFunc("/static/", app.withLogging(func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/static/", http.FileServer(http.Dir("static"))).ServeHTTP(w, r)
	}))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := net.JoinHostPort("127.0.0.1", port)
	log.Printf("SESystem listening on http://%s", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	log.Fatal(srv.ListenAndServe())
}

func initDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}

	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'user'
	);
	CREATE TABLE IF NOT EXISTS clients (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		phone TEXT NOT NULL,
		address TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		client_id INTEGER,
		due_date TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'Pending',
		revenue REAL NOT NULL DEFAULT 0,
		expenses REAL NOT NULL DEFAULT 0,
		notes TEXT,
		FOREIGN KEY(client_id) REFERENCES clients(id) ON DELETE SET NULL
	);
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		expires_at TEXT NOT NULL,
		user_id INTEGER,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user'`); err != nil {
		_ = err
	}
	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN user_id INTEGER`); err != nil {
		_ = err
	}
	if _, err := db.Exec(`UPDATE users SET role = 'admin' WHERE username = 'admin' AND (role IS NULL OR role = '')`); err != nil {
		_ = err
	}
	if _, err := db.Exec(`UPDATE users SET role = 'user' WHERE username = 'user' AND (role IS NULL OR role = '')`); err != nil {
		_ = err
	}

	defaultAdminPassword := "admin123"
	hash, err := hashPassword(defaultAdminPassword)
	if err != nil {
		return nil, err
	}
	seedUsers := `
	INSERT OR IGNORE INTO users (id, username, password, role) VALUES
		(1, 'admin', ?, 'admin');
	`
	if _, err := db.Exec(seedUsers, string(hash)); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE users SET role = 'admin' WHERE username = 'admin'`); err != nil {
		return nil, err
	}
	defaultUserPassword := "user123"
	userHash, err := hashPassword(defaultUserPassword)
	if err != nil {
		return nil, err
	}
	seedRegularUser := `
	INSERT OR IGNORE INTO users (id, username, password, role) VALUES
		(2, 'user', ?, 'user');
	`
	if _, err := db.Exec(seedRegularUser, string(userHash)); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE users SET role = 'user' WHERE username = 'user'`); err != nil {
		return nil, err
	}

	seedClients := `
	INSERT OR IGNORE INTO clients (id, name, phone, address) VALUES
		(1, 'Northwind Logistics', '555-0101', '124 Harbor Road'),
		(2, 'Apex Electric', '555-0102', '88 River Avenue');
	`
	if _, err := db.Exec(seedClients); err != nil {
		return nil, err
	}

	seedJobs := `
	INSERT OR IGNORE INTO jobs (id, title, client_id, due_date, status, revenue, expenses, notes) VALUES
		(1, 'Install backup lighting', 1, '2026-07-06', 'Pending', 450.00, 80.00, 'Customer requested same-day scheduling.'),
		(2, 'Service panel inspection', 2, '2026-07-08', 'In Progress', 320.00, 45.00, 'Parts ordered for inspection.'),
		(3, 'Final wiring check', 1, '2026-07-10', 'Completed', 610.00, 110.00, 'Completed and signed off.');
	`
	if _, err := db.Exec(seedJobs); err != nil {
		return nil, err
	}
	return db, nil
}

func (a *App) withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		log.Printf("%s %s -> %d in %s", r.Method, r.URL.Path, wrapped.statusCode, time.Since(start))
	}
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(statusCode int) {
	lrw.statusCode = statusCode
	lrw.ResponseWriter.WriteHeader(statusCode)
}

func (a *App) render(w http.ResponseWriter, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	tmplPath := filepath.Join("templates", name+".html")
	tmpl, err := template.ParseFiles("templates/base.html", tmplPath)
	if err != nil {
		log.Printf("parse template %s: %v", name, err)
		http.Error(w, "Unable to render page", http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render template %s: %v", name, err)
	}
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.handleLogin(w, r)
		return
	}

	if a.isAuthenticated(r) {
		http.Redirect(w, r, "/jobs", http.StatusSeeOther)
		return
	}

	data := map[string]any{
		"Title":         "SESystem Login",
		"CurrentPage":   "login",
		"ShowPassword":  true,
		"Authenticated": false,
	}
	a.render(w, "login", data)
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := strings.TrimSpace(r.FormValue("password"))
	if username == "" || password == "" {
		data := map[string]any{
			"Title":         "SESystem Login",
			"CurrentPage":   "login",
			"Flash":         "Please enter both your username and password.",
			"ShowPassword":  true,
			"Authenticated": false,
		}
		a.render(w, "login", data)
		return
	}

	user, err := a.getUserByUsername(username)
	if err != nil || !checkPasswordHash(password, user.Password) {
		log.Printf("failed login attempt for username=%s", username)
		data := map[string]any{
			"Title":         "SESystem Login",
			"CurrentPage":   "login",
			"Flash":         "The username or password was incorrect.",
			"ShowPassword":  true,
			"Authenticated": false,
		}
		a.render(w, "login", data)
		return
	}

	sessionID := newSessionID()
	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	if _, err := a.db.Exec(`INSERT INTO sessions (id, expires_at, user_id) VALUES (?, ?, ?)`, sessionID, expiresAt.Format(time.RFC3339), user.ID); err != nil {
		log.Printf("create session: %v", err)
		http.Error(w, "Unable to create a session", http.StatusInternalServerError)
		return
	}

	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE id = ?`, cookie.Value)
	}
	clearedCookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, clearedCookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) requireLocalhost(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLocalhostAddress(r.RemoteAddr) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Forbidden"))
			return
		}
		next(w, r)
	}
}

func isLocalhostAddress(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return false
	}
	if strings.HasPrefix(host, "[") && strings.Contains(host, "]") {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		} else {
			host = strings.Trim(host, "[]")
		}
	} else if strings.Contains(host, ":") {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		} else {
			host = strings.Trim(host, "[]")
		}
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.currentUser(r); !ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.currentUser(r)
		if !ok || user.Role != "admin" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Forbidden"))
			return
		}
		next(w, r)
	}
}

func (a *App) isAuthenticated(r *http.Request) bool {
	_, ok := a.currentUser(r)
	return ok
}

func (a *App) currentUser(r *http.Request) (User, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return User{}, false
	}
	var expiresAt string
	var userID int
	err = a.db.QueryRow(`SELECT expires_at, user_id FROM sessions WHERE id = ?`, cookie.Value).Scan(&expiresAt, &userID)
	if err != nil {
		return User{}, false
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return User{}, false
	}
	if time.Now().After(t) {
		return User{}, false
	}
	user, err := a.getUserByID(userID)
	if err != nil {
		return User{}, false
	}
	return user, true
}

func (a *App) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.handleJobsPost(w, r)
		return
	}

	user, ok := a.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	clients, err := a.listClients()
	if err != nil {
		log.Printf("list clients: %v", err)
		http.Error(w, "Failed to load clients", http.StatusInternalServerError)
		return
	}
	jobs, err := a.listJobs()
	if err != nil {
		log.Printf("list jobs: %v", err)
		http.Error(w, "Failed to load jobs", http.StatusInternalServerError)
		return
	}
	summary := summarizeJobs(jobs)
	data := map[string]any{
		"Title":         "Jobs Dashboard",
		"CurrentPage":   "jobs",
		"Authenticated": true,
		"IsAdmin":       user.Role == "admin",
		"User":          user,
		"Clients":       clients,
		"Jobs":          jobs,
		"Summary":       summary,
	}
	a.render(w, "jobs", data)
}

func (a *App) handleJobsPost(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	switch action {
	case "create":
		title := strings.TrimSpace(r.FormValue("title"))
		clientID, _ := strconv.Atoi(r.FormValue("client_id"))
		dueDate := strings.TrimSpace(r.FormValue("due_date"))
		status := strings.TrimSpace(r.FormValue("status"))
		revenue := parseFloat(r.FormValue("revenue"))
		expenses := parseFloat(r.FormValue("expenses"))
		notes := strings.TrimSpace(r.FormValue("notes"))
		if title == "" || dueDate == "" {
			http.Redirect(w, r, "/jobs?flash=invalid", http.StatusSeeOther)
			return
		}
		if _, err := a.db.Exec(`INSERT INTO jobs (title, client_id, due_date, status, revenue, expenses, notes) VALUES (?, ?, ?, ?, ?, ?, ?)`, title, clientID, dueDate, status, revenue, expenses, notes); err != nil {
			log.Printf("create job: %v", err)
			http.Error(w, "Could not create job", http.StatusInternalServerError)
			return
		}
	case "update":
		id, _ := strconv.Atoi(r.FormValue("id"))
		title := strings.TrimSpace(r.FormValue("title"))
		clientID, _ := strconv.Atoi(r.FormValue("client_id"))
		dueDate := strings.TrimSpace(r.FormValue("due_date"))
		status := strings.TrimSpace(r.FormValue("status"))
		revenue := parseFloat(r.FormValue("revenue"))
		expenses := parseFloat(r.FormValue("expenses"))
		notes := strings.TrimSpace(r.FormValue("notes"))
		if _, err := a.db.Exec(`UPDATE jobs SET title = ?, client_id = ?, due_date = ?, status = ?, revenue = ?, expenses = ?, notes = ? WHERE id = ?`, title, clientID, dueDate, status, revenue, expenses, notes, id); err != nil {
			log.Printf("update job: %v", err)
			http.Error(w, "Could not update job", http.StatusInternalServerError)
			return
		}
	case "delete":
		id, _ := strconv.Atoi(r.FormValue("id"))
		if _, err := a.db.Exec(`DELETE FROM jobs WHERE id = ?`, id); err != nil {
			log.Printf("delete job: %v", err)
			http.Error(w, "Could not delete job", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (a *App) handleClients(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.handleClientsPost(w, r)
		return
	}
	user, ok := a.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	clients, err := a.listClients()
	if err != nil {
		log.Printf("list clients: %v", err)
		http.Error(w, "Failed to load clients", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Title":         "Clients",
		"CurrentPage":   "clients",
		"Authenticated": true,
		"IsAdmin":       user.Role == "admin",
		"User":          user,
		"Clients":       clients,
	}
	a.render(w, "clients", data)
}

func (a *App) handleClientsPost(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	switch action {
	case "create":
		name := strings.TrimSpace(r.FormValue("name"))
		phone := strings.TrimSpace(r.FormValue("phone"))
		address := strings.TrimSpace(r.FormValue("address"))
		if name == "" || phone == "" || address == "" {
			http.Redirect(w, r, "/clients", http.StatusSeeOther)
			return
		}
		if _, err := a.db.Exec(`INSERT INTO clients (name, phone, address) VALUES (?, ?, ?)`, name, phone, address); err != nil {
			log.Printf("create client: %v", err)
			http.Error(w, "Could not create client", http.StatusInternalServerError)
			return
		}
	case "update":
		id, _ := strconv.Atoi(r.FormValue("id"))
		name := strings.TrimSpace(r.FormValue("name"))
		phone := strings.TrimSpace(r.FormValue("phone"))
		address := strings.TrimSpace(r.FormValue("address"))
		if _, err := a.db.Exec(`UPDATE clients SET name = ?, phone = ?, address = ? WHERE id = ?`, name, phone, address, id); err != nil {
			log.Printf("update client: %v", err)
			http.Error(w, "Could not update client", http.StatusInternalServerError)
			return
		}
	case "delete":
		id, _ := strconv.Atoi(r.FormValue("id"))
		if _, err := a.db.Exec(`DELETE FROM clients WHERE id = ?`, id); err != nil {
			log.Printf("delete client: %v", err)
			http.Error(w, "Could not delete client", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/clients", http.StatusSeeOther)
}

func (a *App) handleFinances(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.handleFinancesPost(w, r)
		return
	}
	jobs, err := a.listJobs()
	if err != nil {
		log.Printf("list jobs for finances: %v", err)
		http.Error(w, "Failed to load finances", http.StatusInternalServerError)
		return
	}
	summary := summarizeJobs(jobs)
	data := map[string]any{
		"Title":         "Finances",
		"CurrentPage":   "finances",
		"Authenticated": true,
		"IsAdmin":       true,
		"Jobs":          jobs,
		"Summary":       summary,
	}
	a.render(w, "finances", data)
}

func (a *App) handleFinancesPost(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("id"))
	revenue := parseFloat(r.FormValue("revenue"))
	expenses := parseFloat(r.FormValue("expenses"))
	if _, err := a.db.Exec(`UPDATE jobs SET revenue = ?, expenses = ? WHERE id = ?`, revenue, expenses, id); err != nil {
		log.Printf("update finances: %v", err)
		http.Error(w, "Could not update finances", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/finances", http.StatusSeeOther)
}

func (a *App) handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.handleUsersPost(w, r)
		return
	}
	users, err := a.listUsers()
	if err != nil {
		log.Printf("list users: %v", err)
		http.Error(w, "Failed to load users", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Title":         "Users",
		"CurrentPage":   "users",
		"Authenticated": true,
		"IsAdmin":       true,
		"Users":         users,
	}
	a.render(w, "users", data)
}

func (a *App) handleUsersPost(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	switch action {
	case "create":
		username := strings.TrimSpace(r.FormValue("username"))
		password := strings.TrimSpace(r.FormValue("password"))
		role := strings.TrimSpace(r.FormValue("role"))
		if username == "" || password == "" {
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}
		if role == "" {
			role = "user"
		}
		hash, err := hashPassword(password)
		if err != nil {
			log.Printf("hash user password: %v", err)
			http.Error(w, "Could not create user", http.StatusInternalServerError)
			return
		}
		if _, err := a.db.Exec(`INSERT INTO users (username, password, role) VALUES (?, ?, ?)`, username, string(hash), role); err != nil {
			log.Printf("create user: %v", err)
			http.Error(w, "Could not create user", http.StatusInternalServerError)
			return
		}
	case "update":
		id, _ := strconv.Atoi(r.FormValue("id"))
		username := strings.TrimSpace(r.FormValue("username"))
		password := strings.TrimSpace(r.FormValue("password"))
		role := strings.TrimSpace(r.FormValue("role"))
		if username == "" || role == "" {
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}
		if password != "" {
			hash, err := hashPassword(password)
			if err != nil {
				log.Printf("hash updated password: %v", err)
				http.Error(w, "Could not update user", http.StatusInternalServerError)
				return
			}
			if _, err := a.db.Exec(`UPDATE users SET username = ?, password = ?, role = ? WHERE id = ?`, username, string(hash), role, id); err != nil {
				log.Printf("update user: %v", err)
				http.Error(w, "Could not update user", http.StatusInternalServerError)
				return
			}
		} else if _, err := a.db.Exec(`UPDATE users SET username = ?, role = ? WHERE id = ?`, username, role, id); err != nil {
			log.Printf("update user: %v", err)
			http.Error(w, "Could not update user", http.StatusInternalServerError)
			return
		}
	case "delete":
		id, _ := strconv.Atoi(r.FormValue("id"))
		if _, err := a.db.Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
			log.Printf("delete user: %v", err)
			http.Error(w, "Could not delete user", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (a *App) handleReceipts(w http.ResponseWriter, r *http.Request) {
	user, ok := a.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	jobs, err := a.listJobs()
	if err != nil {
		log.Printf("list receipts: %v", err)
		http.Error(w, "Failed to load receipts", http.StatusInternalServerError)
		return
	}
	completed := []Job{}
	for _, job := range jobs {
		if job.Status == "Completed" {
			completed = append(completed, job)
		}
	}
	data := map[string]any{
		"Title":         "Receipts",
		"CurrentPage":   "receipts",
		"Authenticated": true,
		"IsAdmin":       user.Role == "admin",
		"User":          user,
		"Jobs":          completed,
	}
	a.render(w, "receipts", data)
}

func (a *App) handleSpreadsheets(w http.ResponseWriter, r *http.Request) {
	user, ok := a.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	jobs, err := a.listJobs()
	if err != nil {
		log.Printf("list spreadsheets: %v", err)
		http.Error(w, "Failed to load spreadsheets", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Title":         "Spreadsheets",
		"CurrentPage":   "spreadsheets",
		"Authenticated": true,
		"IsAdmin":       user.Role == "admin",
		"User":          user,
		"Jobs":          jobs,
		"Summary":       summarizeJobs(jobs),
	}
	a.render(w, "spreadsheets", data)
}

func (a *App) handlePublicReceipt(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/receipt/"), "/")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	job, err := a.getJob(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{
		"Title":         "Receipt",
		"CurrentPage":   "receipts",
		"Authenticated": false,
		"Job":           job,
	}
	a.render(w, "receipt", data)
}

func (a *App) getUserByUsername(username string) (User, error) {
	var user User
	err := a.db.QueryRow(`SELECT id, username, password, role FROM users WHERE username = ?`, username).Scan(&user.ID, &user.Username, &user.Password, &user.Role)
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (a *App) getUserByID(id int) (User, error) {
	var user User
	err := a.db.QueryRow(`SELECT id, username, password, role FROM users WHERE id = ?`, id).Scan(&user.ID, &user.Username, &user.Password, &user.Role)
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (a *App) listUsers() ([]User, error) {
	rows, err := a.db.Query(`SELECT id, username, password, role FROM users ORDER BY username ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Password, &u.Role); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func checkPasswordHash(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (a *App) listClients() ([]Client, error) {
	rows, err := a.db.Query(`SELECT id, name, phone, address FROM clients ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := []Client{}
	for rows.Next() {
		var c Client
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Address); err != nil {
			return nil, err
		}
		clients = append(clients, c)
	}
	return clients, rows.Err()
}

func (a *App) listJobs() ([]Job, error) {
	rows, err := a.db.Query(`
		SELECT j.id, j.title, j.client_id, COALESCE(c.name, ''), j.due_date, j.status, j.revenue, j.expenses, j.notes
		FROM jobs j
		LEFT JOIN clients c ON c.id = j.client_id
		ORDER BY j.due_date ASC, j.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		var j Job
		var clientID sql.NullInt64
		if err := rows.Scan(&j.ID, &j.Title, &clientID, &j.ClientName, &j.DueDate, &j.Status, &j.Revenue, &j.Expenses, &j.Notes); err != nil {
			return nil, err
		}
		if clientID.Valid {
			j.ClientID = int(clientID.Int64)
		}
		j.Profit = j.Revenue - j.Expenses
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (a *App) getJob(id int) (Job, error) {
	var j Job
	var clientID sql.NullInt64
	err := a.db.QueryRow(`
		SELECT j.id, j.title, j.client_id, COALESCE(c.name, ''), j.due_date, j.status, j.revenue, j.expenses, j.notes
		FROM jobs j
		LEFT JOIN clients c ON c.id = j.client_id
		WHERE j.id = ?`, id).Scan(&j.ID, &j.Title, &clientID, &j.ClientName, &j.DueDate, &j.Status, &j.Revenue, &j.Expenses, &j.Notes)
	if err != nil {
		return Job{}, err
	}
	if clientID.Valid {
		j.ClientID = int(clientID.Int64)
	}
	j.Profit = j.Revenue - j.Expenses
	return j, nil
}

func summarizeJobs(jobs []Job) Summary {
	summary := Summary{}
	for _, job := range jobs {
		summary.TotalRevenue += job.Revenue
		summary.TotalExpenses += job.Expenses
		summary.TotalProfit += job.Profit
		switch job.Status {
		case "Pending":
			summary.Pending++
		case "In Progress":
			summary.InProgress++
		case "Completed":
			summary.Completed++
		}
	}
	return summary
}

func parseFloat(val string) float64 {
	result, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0
	}
	return result
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func newSessionID() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
