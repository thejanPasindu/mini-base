package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/kernel"
	"github.com/mini-base/mini-base/internal/modules/admin"
)

const adminUsage = `Usage: minibase admin <command> [flags]

Commands:
  create          --email EMAIL [--password-stdin]   Add a dashboard admin
  list                                                List admins
  delete          --email EMAIL                       Remove an admin
  reset-password  --email EMAIL [--password-stdin]   Set a new password

Without --password-stdin a strong password is generated and printed once.
In Docker:  docker compose exec minibase /minibase admin create --email you@example.com
`

// adminCmd runs `minibase admin ...`. It only needs the database, not a
// running server, and always applies the admin module's migrations.
func adminCmd(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return flag.ErrHelp
	}
	sub, args := args[0], args[1:]

	fs := flag.NewFlagSet("admin "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "admin email")
	fromStdin := fs.Bool("password-stdin", false, "read the password from standard input")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Only the admin module's tables are needed.
	env := func(k string) string {
		if k == kernel.EnvPrefix+"MODULES" {
			return "admin"
		}
		return getenv(k)
	}
	cfg, err := kernel.LoadConfig(env)
	if err != nil {
		return err
	}
	cfg.Log.Level = slog.LevelError // quiet: errors only
	k := kernel.New(cfg, registry(), kernel.NewLogger(cfg.Log, stderr), version, env)

	return k.WithDB(ctx, func(ctx context.Context, db *pgxpool.Pool) error {
		cli, err := admin.NewCLI(db)
		if err != nil {
			return err
		}
		switch sub {
		case "list":
			list, err := cli.List(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "EMAIL\tCREATED\tLAST SIGN-IN")
			for _, a := range list {
				last := "never"
				if a.LastSignInAt != nil {
					last = a.LastSignInAt.Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Email, a.CreatedAt.Format("2006-01-02 15:04"), last)
			}
			return tw.Flush()

		case "create", "reset-password":
			if *email == "" {
				return errors.New("--email is required")
			}
			pw, generated, err := readPassword(*fromStdin, stdin)
			if err != nil {
				return err
			}
			if sub == "create" {
				if _, err := cli.Create(ctx, *email, pw); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Admin %s created.\n", *email)
			} else {
				if err := cli.ResetPassword(ctx, *email, pw); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Password for %s reset; existing sessions were signed out.\n", *email)
			}
			if generated {
				fmt.Fprintf(stdout, "Password: %s\n(shown once — store it in a password manager)\n", pw)
			}
			fmt.Fprintln(stdout, "Sign in at http://<your-host>:8090/_/")
			return nil

		case "delete":
			if *email == "" {
				return errors.New("--email is required")
			}
			if err := cli.Delete(ctx, *email); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Admin %s deleted.\n", *email)
			return nil
		}
		fmt.Fprint(stderr, adminUsage)
		return fmt.Errorf("unknown admin command %q", sub)
	})
}

func readPassword(fromStdin bool, stdin io.Reader) (pw string, generated bool, err error) {
	if !fromStdin {
		pw, err := admin.GeneratePassword()
		return pw, true, err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), false, nil
}
