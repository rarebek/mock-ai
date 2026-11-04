// Provision a separate admin identity. No public signup or student promotion.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	app "github.com/own/mock-ai/apps/api/internal/admin/application"
	"github.com/own/mock-ai/apps/api/internal/admin/postgres"
	"io"
	"os"
	"strings"
	"time"
)

func run() error {
	email := flag.String("email", "", "Admin login email")
	name := flag.String("name", "", "Admin display name")
	stdin := flag.Bool("password-stdin", false, "Read password from standard input (never command arguments)")
	flag.Parse()
	if !*stdin {
		return fmt.Errorf("use --email, --name and --password-stdin; existing accounts are never overwritten")
	}
	secret, e := io.ReadAll(io.LimitReader(os.Stdin, 1026))
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return fmt.Errorf("database configuration invalid")
	}
	defer pool.Close()
	_, e = app.New(postgres.New(pool, "")).Create(ctx, *email, *name, strings.TrimRight(string(secret), "\r\n"))
	if e != nil {
		return fmt.Errorf("admin creation failed (check input, database and duplicate email): %w", e)
	}
	fmt.Println("Separate admin account created. No student account was changed.")
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
