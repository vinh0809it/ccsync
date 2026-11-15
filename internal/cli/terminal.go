package cli

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/vinhvh0809/ccsync/internal/app"
	"github.com/vinhvh0809/ccsync/internal/store"
)

// unlock asks for the passphrase and opens the configured store.
func (c *cli) unlock() (*app.Client, error) {
	if _, err := store.LoadConfig(); err != nil {
		return nil, err
	}
	pass, err := c.passphrase(false)
	if err != nil {
		return nil, err
	}
	st, err := store.Open()
	if err != nil {
		return nil, err
	}
	return app.Unlock(st, pass)
}

// passphrase reads $CCSYNC_PASSPHRASE, or prompts on the terminal without
// echo (twice when confirm is set).
func (c *cli) passphrase(confirm bool) (string, error) {
	if p := os.Getenv("CCSYNC_PASSPHRASE"); p != "" {
		return p, nil
	}
	p, err := c.readSecret("Store passphrase: ")
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", fmt.Errorf("empty passphrase")
	}
	if confirm {
		again, err := c.readSecret("Confirm passphrase: ")
		if err != nil {
			return "", err
		}
		if again != p {
			return "", fmt.Errorf("passphrases do not match")
		}
	}
	return p, nil
}

func (c *cli) readSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no terminal to ask for the passphrase — set CCSYNC_PASSPHRASE")
	}
	fmt.Fprint(c.err, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(c.err)
	return string(b), err
}
