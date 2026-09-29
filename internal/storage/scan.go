package storage

// scannable is satisfied by *sql.Rows and *sql.Row, allowing scan helpers to
// accept either without depending on a concrete type.
type scannable interface {
	Scan(dest ...any) error
}
