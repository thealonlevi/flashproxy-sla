module github.com/flashproxy/flashproxy-status

go 1.25

// Intentionally dependency-free: ClickHouse is accessed over its HTTP interface
// using only the standard library, so the whole app builds offline and is
// trivially reproducible.
