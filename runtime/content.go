package runtime

// ContentType represents payload MIME types, supporting custom strings alongside standard constants.
type ContentType = string

const (
	JSON   ContentType = "application/json"
	NDJSON ContentType = "application/x-ndjson"
	CSV    ContentType = "text/csv"
	TEXT   ContentType = "text/plain"
	BYTES  ContentType = "application/octet-stream"
)

// isRows checks if the content type is a stream of rows rather than a single document.
func isRows(contentType string) bool {
	return contentType == NDJSON || contentType == CSV
}
