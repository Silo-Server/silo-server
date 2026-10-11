package auth

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Capture the production initializer before TestMain lowers the cost.
var productionPasswordHashCost = passwordHashCost

// Every fixture account has its own row; only the password hash is shared.
var externalSignInPasswordHash string

func TestMain(m *testing.M) {
	passwordHashCost = bcrypt.MinCost
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery"), passwordHashCost)
	if err != nil {
		panic(err)
	}
	externalSignInPasswordHash = string(hash)
	os.Exit(m.Run())
}

func TestPasswordHashCostDefault(t *testing.T) {
	if productionPasswordHashCost != bcrypt.DefaultCost {
		t.Fatalf("production password hash cost = %d, want bcrypt.DefaultCost (%d)", productionPasswordHashCost, bcrypt.DefaultCost)
	}
}

func TestExternalSignInPasswordHashCost(t *testing.T) {
	assertTestPasswordHash(t, externalSignInPasswordHash, "correct horse battery")
}

func assertTestPasswordHash(t *testing.T, hash, password string) {
	t.Helper()
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost() error = %v, want nil", err)
	}
	if cost != bcrypt.MinCost {
		t.Fatalf("bcrypt.Cost() = %d, want bcrypt.MinCost (%d)", cost, bcrypt.MinCost)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		t.Fatalf("matching password error = %v, want nil", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong password")); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("wrong password error = %v, want %v", err, bcrypt.ErrMismatchedHashAndPassword)
	}
}
