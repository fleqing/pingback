package main

import (
	"os"

	"pingback/internal/app"
	"pingback/internal/keychain"
)

func main() {
	deps := app.Deps{Passwords: keychain.Source{}}
	os.Exit(app.RunWith(deps, os.Args[1:], os.Environ(), os.Stdout, os.Stderr))
}
