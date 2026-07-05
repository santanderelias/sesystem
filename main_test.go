package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestPasswordHashingAndValidation(t *testing.T) {
	hash, err := hashPassword("demo123")
	if err != nil {
		t.Fatalf("hashPassword returned error: %v", err)
	}
	if !checkPasswordHash("demo123", hash) {
		t.Fatal("expected password to validate")
	}
	if checkPasswordHash("wrong", hash) {
		t.Fatal("expected wrong password to fail")
	}
}

func TestIsLocalhostAddress(t *testing.T) {
	cases := []struct {
		name     string
		addr     string
		expected bool
	}{
		{name: "ipv4 loopback", addr: "127.0.0.1:54321", expected: true},
		{name: "ipv6 loopback", addr: "[::1]:54321", expected: true},
		{name: "localhost", addr: "localhost:54321", expected: true},
		{name: "private network", addr: "192.168.1.10:54321", expected: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLocalhostAddress(tc.addr); got != tc.expected {
				t.Fatalf("isLocalhostAddress(%q) = %v, want %v", tc.addr, got, tc.expected)
			}
		})
	}
}

func TestRequireLocalhostBlocksNonLocalhost(t *testing.T) {
	app := &App{}
	handler := app.requireLocalhost(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("expected non-localhost requests to be blocked")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	rr := httptest.NewRecorder()

	handler(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestInitDBSeedsRolesCorrectly(t *testing.T) {
	dbPath := "test-seed-roles.db"
	db, err := initDB(dbPath)
	if err != nil {
		t.Fatalf("initDB returned error: %v", err)
	}
	defer db.Close()
	defer os.Remove(dbPath)

	var adminRole string
	if err := db.QueryRow(`SELECT role FROM users WHERE username = 'admin'`).Scan(&adminRole); err != nil {
		t.Fatalf("query admin role: %v", err)
	}
	if adminRole != "admin" {
		t.Fatalf("expected admin role to be admin, got %q", adminRole)
	}

	var userRole string
	if err := db.QueryRow(`SELECT role FROM users WHERE username = 'user'`).Scan(&userRole); err != nil {
		t.Fatalf("query regular user role: %v", err)
	}
	if userRole != "user" {
		t.Fatalf("expected regular user role to be user, got %q", userRole)
	}
}

func TestRequireAdminBlocksNonAdmin(t *testing.T) {
	dbPath := "test-role.db"
	db, err := initDB(dbPath)
	if err != nil {
		t.Fatalf("initDB returned error: %v", err)
	}
	defer db.Close()
	defer os.Remove(dbPath)

	app := &App{db: db}
	handler := app.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("expected non-admin requests to be blocked")
	})

	_, err = db.Exec(`INSERT INTO users (id, username, password, role) VALUES (?, ?, ?, ?)`, 99, "regular", "hashed", "user")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	sessionID := newSessionID()
	_, err = db.Exec(`INSERT INTO sessions (id, expires_at, user_id) VALUES (?, ?, ?)`, sessionID, time.Now().Add(time.Hour).Format(time.RFC3339), 99)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	rr := httptest.NewRecorder()

	handler(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
	}
}
