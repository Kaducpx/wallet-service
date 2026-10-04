// Package migrations embute os arquivos SQL versionados no binário, para que
// o comando "wallet-service migrate" funcione sem depender do checkout.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
