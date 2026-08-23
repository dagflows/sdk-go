package runtime

import (
	"bytes"
	"fmt"
	"strings"
)

// Multipart contains presigned URLs and configuration for multipart uploads.
type Multipart struct {
	UploadID    string
	PartSize    int64
	PartURLs    []string
	CompleteURL string
	AbortURL    string
}

// multipartFromCtx parses the output_multipart field from the execution context.
func multipartFromCtx(raw any) *Multipart {
	entry, ok := raw.(map[string]any)
	if !ok {
		return nil
	}

	urls, _ := entry["part_urls"].([]any)
	partSize := Num(entry["part_size"], 0)
	complete, _ := entry["complete_url"].(string)

	if len(urls) == 0 || complete == "" || partSize <= 0 {
		return nil
	}

	partURLs := make([]string, 0, len(urls))
	for _, u := range urls {
		partURLs = append(partURLs, Str(u))
	}

	return &Multipart{
		UploadID:    Str(entry["upload_id"]),
		PartSize:    partSize,
		PartURLs:    partURLs,
		CompleteURL: complete,
		AbortURL:    Str(entry["abort_url"]),
	}
}

// Capacity calculates the maximum byte capacity supported across all presigned parts.
func (m *Multipart) Capacity() int64 {
	return int64(len(m.PartURLs)) * m.PartSize
}

// PartUploader buffers output into fixed-size chunks and uploads each via presigned PUT.
type PartUploader struct {
	upload      *Multipart
	contentType ContentType
	buffer      bytes.Buffer
	etags       []string
	sent        int64
}

// NewPartUploader creates a new multipart uploader instance.
func NewPartUploader(upload *Multipart, contentType ContentType) *PartUploader {
	return &PartUploader{
		upload:      upload,
		contentType: contentType,
	}
}

func (p *PartUploader) PartsSent() int {
	return len(p.etags)
}

// BytesSent returns total bytes uploaded to storage excluding uncommitted buffer tail.
func (p *PartUploader) BytesSent() int64 {
	return p.sent
}

// Write buffers data and flushes full parts to object storage.
func (p *PartUploader) Write(chunk []byte) error {
	p.buffer.Write(chunk)

	size := int(p.upload.PartSize)

	for p.buffer.Len() >= size {
		if err := p.send(p.buffer.Next(size)); err != nil {
			return err
		}
	}

	if p.buffer.Len() == 0 {
		p.buffer.Reset()
	}

	return nil
}

// Finish flushes remaining buffer data and sends the complete multipart upload request.
func (p *PartUploader) Finish() (int64, error) {
	if p.buffer.Len() > 0 {
		if err := p.send(p.buffer.Bytes()); err != nil {
			return 0, err
		}

		p.buffer.Reset()
	}

	if len(p.etags) == 0 {
		p.Abort()

		return 0, nil
	}

	if _, err := post(p.upload.CompleteURL, p.completion(), "application/xml"); err != nil {
		return 0, err
	}

	return p.sent, nil
}

// Abort triggers the abort URL to discard uncommitted parts from object storage.
func (p *PartUploader) Abort() {
	if p.upload.AbortURL != "" {
		del(p.upload.AbortURL)
	}
}

func (p *PartUploader) send(body []byte) error {
	number := len(p.etags) + 1

	if number > len(p.upload.PartURLs) {
		return &OutputTooLarge{
			Message: fmt.Sprintf(
				"this node can emit at most %d bytes and the output passed it; raise max_output_mb on the node, and check the plan allows it",
				p.upload.Capacity(),
			),
		}
	}

	etag, err := put(p.upload.PartURLs[number-1], body, p.contentType)
	if err != nil {
		return err
	}

	if etag == "" {
		return &OutputTooLarge{
			Message: fmt.Sprintf(
				"part %d was stored but returned no ETag, so the upload cannot be completed; this is a storage misconfiguration rather than a node problem",
				number,
			),
		}
	}

	p.etags = append(p.etags, etag)
	p.sent += int64(len(body))

	return nil
}

// xmlEscape handles XML character escaping for ETag values.
var xmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// completion generates the CompleteMultipartUpload XML payload expected by S3 and R2.
func (p *PartUploader) completion() []byte {
	var out bytes.Buffer

	out.WriteString("<CompleteMultipartUpload>")

	for i, etag := range p.etags {
		fmt.Fprintf(&out, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", i+1, xmlEscape.Replace(etag))
	}

	out.WriteString("</CompleteMultipartUpload>")

	return out.Bytes()
}
