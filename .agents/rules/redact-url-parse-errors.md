# Never let url.Parse errors echo the input URL in error messages

`url.Parse` returns `*url.Error`, whose `Error()` method embeds the raw input
verbatim — including any password in the userinfo component. Wrapping such an error
directly with `%w` or `%v` will leak connection credentials into deploy logs, crash
reporters, and Pulumi state. Always unwrap with `errors.As(err, &ue)` and emit only
`ue.Err` (the parse reason), explicitly noting that the URL was redacted. The pattern
lives in `redactParseErr` in `providers/neon/cmd/pulumi-resource-neon/resources/neon_role.go`.

## Applies to

Any code in `providers/` (or elsewhere) that calls `url.Parse` on a value that could
contain a password (connection URIs, API endpoint overrides with credentials, etc.).

## Example

```go
// WRONG: leaks the raw URI (including password) into the error message
u, err := url.Parse(connURI)
if err != nil {
    return fmt.Errorf("parse URI: %w", err) // *url.Error embeds connURI verbatim
}

// CORRECT: emit only the parse reason, drop the URL
u, err := url.Parse(connURI)
if err != nil {
    var ue *url.Error
    if errors.As(err, &ue) {
        return fmt.Errorf("parse URI: %v (url redacted)", ue.Err)
    }
    return fmt.Errorf("parse URI: %w", err)
}
```
