package migrations

import "embed"

// FS contém os pares *.up.sql / *.down.sql versionados.
//
//go:embed *.up.sql *.down.sql
var FS embed.FS
