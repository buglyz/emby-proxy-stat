package main

import (
	"testing"
	"time"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := generatePasswordHash("correct horse battery staple")
	if err != nil {
		t.Fatalf("generate hash: %v", err)
	}
	cfg := Config{}
	cfg.Auth.Username = "admin"
	cfg.Auth.PasswordHash = hash
	if !verifyPassword("correct horse battery staple", cfg) {
		t.Fatal("expected password to verify")
	}
	if verifyPassword("wrong password", cfg) {
		t.Fatal("unexpected password verification")
	}
}

func TestValidateConfigRejectsEmptyCredentials(t *testing.T) {
	cfg := Config{}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected empty credentials to be rejected")
	}
}

func TestReportDue(t *testing.T) {
	before := time.Date(2026, 9, 7, 23, 58, 0, 0, businessLocation)
	after := time.Date(2026, 9, 7, 23, 59, 1, 0, businessLocation)
	if reportDue(before, "23:59") {
		t.Fatal("report should not be due before configured time")
	}
	if !reportDue(after, "23:59") {
		t.Fatal("report should be due after configured time")
	}
}
