// Command guards verifies that regression guards fail when intentional bugs are reintroduced.
//
//	go run ./scripts/guards            # every guard
//	go run ./scripts/guards -only csv  # guards whose name contains csv
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type guard struct {
	name string
	file string
	old  string
	new  string
	pkg  string
	run  string
}

var guards = []guard{
	{
		name: "json array: an element that ends mid value is reported as invalid",
		file: "internal/runtime/jsonstream.go",
		old:  `return errors.New("the reference is not valid JSON, the document ends mid value")`,
		new:  `return errors.New("the array is never closed, the reference ended mid document")`,
		pkg:  "./internal/runtime", run: "TestATruncatedDocumentIsDetectedAtEOF",
	},
	{
		name: "json array: an object is refused by name",
		file: "internal/runtime/jsonstream.go",
		old:  `"expected a JSON array, found %q, a JSON object cannot be streamed as rows, so send ndjson or read it with .value()",`,
		new:  `"expected a JSON array, found %q, a JSON object is not an array, so send ndjson or read it with .value()",`,
		pkg:  "./internal/runtime", run: "TestAJSONObjectIsRefusedByName",
	},
	{
		name: "encoders: row by row bytes equal one shot bytes",
		file: "internal/runtime/encode.go",
		old:  "\tprefix := byte(',')\n\tif !e.started {",
		new:  "\tprefix := byte('[')\n\tif !e.started {",
		pkg:  "./internal/runtime", run: "TestStreamedAndBufferedEncodingsAreIdentical",
	},
	{
		name: "encoders: csv records end with CRLF",
		file: "internal/runtime/encode.go",
		old:  "\tout.WriteString(\"\\r\\n\")\n}",
		new:  "\tout.WriteString(\"\\n\")\n}",
		pkg:  "./internal/runtime", run: "TestCSVTakesItsHeaderFromTheFirstRowAndEndsLinesWithCRLF",
	},
	{
		name: "encoders: the row encoder's csv records end with CRLF too",
		file: "internal/runtime/encode.go",
		old:  "\te.buf.Truncate(e.buf.Len() - 1)\n\te.buf.WriteString(\"\\r\\n\")",
		new:  "",
		pkg:  "./internal/runtime", run: "TestStreamedAndBufferedEncodingsAreIdentical",
	},
	{
		name: "encoders: html is never escaped",
		file: "internal/runtime/json.go",
		old:  "c.enc.SetEscapeHTML(false)",
		new:  "c.enc.SetEscapeHTML(true)",
		pkg:  "./internal/runtime", run: "TestEncodingNeverEscapesHTMLAndMeasuresBytesNotCharacters",
	},
	{
		name: "inputs: materialising twice costs one download",
		file: "internal/runtime/envelope.go",
		old:  "in.held, in.hasValue = value, true",
		new:  "_ = value",
		pkg:  "./internal/runtime", run: "TestMaterialisingTwiceCostsOneDownload",
	},
	{
		name: "inputs: the size guard refuses before downloading",
		file: "internal/runtime/envelope.go",
		old:  "if budget > 0 && in.Size()*ParseExpansion > budget {",
		new:  "if budget < 0 && in.Size()*ParseExpansion > budget {",
		pkg:  "./internal/runtime", run: "TestAValueThatWouldNotFitIsRefusedBeforeItIsFetched",
	},
	{
		name: "inputs: iteration re-opens the source",
		file: "internal/runtime/envelope.go",
		old:  "\t\tbody, err := stream(in.URL())\n\t\tif err != nil {\n\t\t\tyield(nil, err)\n\t\t\treturn\n\t\t}\n\n\t\tdefer body.Close()\n\n\t\tdecode(body)(yield)",
		new:  "\t\tbody, err := stream(in.URL())\n\t\tif err != nil {\n\t\t\tyield(nil, err)\n\t\t\treturn\n\t\t}\n\n\t\tdefer body.Close()\n\n\t\tcollected := []any{}\n\t\tfor row, err := range decode(body) {\n\t\t\tif err != nil {\n\t\t\t\tyield(nil, err)\n\t\t\t\treturn\n\t\t\t}\n\t\t\tcollected = append(collected, row)\n\t\t}\n\t\tin.held, in.hasValue = collected, true\n\t\tseqOf(collected)(yield)",
		pkg:  "./internal/runtime", run: "TestIterationIsReEntrantRatherThanASpentGenerator",
	},
	{
		name: "inputs: a missing key lists the parents",
		file: "internal/runtime/envelope.go",
		old:  `"no input named '%s', this node's parents are: %s", key, in.available())`,
		new:  `"no input named '%s'", key)`,
		pkg:  "./internal/runtime", run: "TestAMissingInputNamesTheParentsThatExist",
	},
	{
		name: "result: a value exactly at the limit inlines",
		file: "internal/runtime/result.go",
		old:  "if inlineSize > limit {",
		new:  "if inlineSize >= limit {",
		pkg:  "./internal/runtime", run: "TestSizeIsMeasuredInBytesNotCharacters",
	},
	{
		name: "result: the running total counts the commas",
		file: "internal/runtime/result.go",
		old:  "\tif len(s.collected) > 0 {\n\t\tsize++\n\t}",
		new:  "",
		pkg:  "./internal/runtime", run: "TestRowCollectionCountsTheArrayPunctuation",
	},
	{
		name: "result: a blank routing key is refused",
		file: "internal/runtime/result.go",
		old:  `if strings.TrimSpace(key) == "" {`,
		new:  `if strings.TrimSpace(key) == "\x00" {`,
		pkg:  "./internal/runtime", run: "TestABlankRoutingKeyIsRefused",
	},
	{
		name: "result: the buffer cap budgets decoded values, not encoded bytes",
		file: "internal/runtime/result.go",
		old:  "return max(limit, int64(heap/ParseExpansion))",
		new:  "return max(limit, int64(heap))",
		pkg:  "./internal/runtime", run: "TestRowOutputRefusesBeforeItCouldExceedGuestMemory",
	},
	{
		name: "result: a panic mid stream aborts the upload",
		file: "internal/runtime/result.go",
		old:  "\tcommitted := false\n\tdefer func() {\n\t\tif !committed {\n\t\t\tsink.Abort()\n\t\t}\n\t}()\n",
		new:  "\tcommitted := false\n\t_ = committed\n",
		pkg:  "./internal/runtime", run: "TestAPanicMidStreamAbortsTheUploadToo",
	},
	{
		name: "result: rows stop being collected past the cap",
		file: "internal/runtime/result.go",
		old:  "\tif s.measured > s.cap {\n\t\treturn &OutputTooLarge{",
		new:  "\tif false {\n\t\treturn &OutputTooLarge{",
		pkg:  "./internal/runtime", run: "TestRowsStopBeingCollectedOnceTheyCannotBeInline",
	},
	{
		name: "http: a rejected signature echoed by storage is scrubbed",
		file: "internal/runtime/http.go",
		old:  "\t\t\tif len(value) >= 8 {\n\t\t\t\ttext = strings.ReplaceAll(text, value, \"[scrubbed]\")\n\t\t\t}",
		new:  "\t\t\tif len(value) >= 8 {\n\t\t\t\ttext = strings.ReplaceAll(text, value, value)\n\t\t\t}",
		pkg:  "./internal/runtime", run: "TestAnErrorStatusCarriesTheProvidersDiagnosisWithTheSignatureScrubbed",
	},
	{
		name: "http: the provider's diagnosis reaches the message",
		file: "internal/runtime/http.go",
		old:  "\treturn fmt.Errorf(\"HTTP %s: %s\", status, text)",
		new:  "\treturn fmt.Errorf(\"HTTP %s\", status)",
		pkg:  "./internal/runtime", run: "TestAnErrorStatusCarriesTheProvidersDiagnosisWithTheSignatureScrubbed",
	},
	{
		name: "http: the signature never reaches an error",
		file: "internal/runtime/http.go",
		old:  `before, _, _ := strings.Cut(rawURL, "?")`,
		new:  `before, _, _ := strings.Cut(rawURL, "#")`,
		pkg:  "./internal/runtime", run: "TestAFailureNeverLogsTheSignature",
	},
	{
		name: "http: an error status is a failure",
		file: "internal/runtime/http.go",
		old:  "if resp.StatusCode >= 400 {",
		new:  "if resp.StatusCode >= 600 {",
		pkg:  "./internal/runtime", run: "TestAFailedReadIsRetryableOnTheWorkflowPolicy",
	},
	{
		name: "http: a moving body is never cut",
		file: "internal/runtime/http.go",
		old:  "\tb.watchdog.Reset(httpTimeout)\n",
		new:  "",
		pkg:  "./internal/runtime", run: "TestABodyThatKeepsMovingIsNeverCutByTheTimeout",
	},
	{
		name: "http: network failures are retryable infrastructure",
		file: "internal/runtime/http.go",
		old:  "\t\tCategory: INFRASTRUCTURE,\n\t\tAbort:    new(false),\n\t}\n}\n\n// uploadedNothing",
		new:  "\t\tCategory: PERMANENT,\n\t}\n}\n\n// uploadedNothing",
		pkg:  "./internal/runtime", run: "TestAFailedReadIsRetryableOnTheWorkflowPolicy",
	},
	{
		name: "multipart: an empty upload aborts instead of completing",
		file: "internal/runtime/multipart.go",
		old:  "\tif len(p.etags) == 0 {\n\t\tp.Abort()\n\n\t\treturn 0, nil\n\t}",
		new:  "",
		pkg:  "./internal/runtime", run: "TestAnEmptyUploadIsAbortedNotCompleted",
	},
	{
		name: "multipart: a part without an ETag is a hard failure",
		file: "internal/runtime/multipart.go",
		old:  `if etag == "" {`,
		new:  `if false {`,
		pkg:  "./internal/runtime", run: "TestAPartWithoutAnETagIsAStorageMisconfiguration",
	},
	{
		name: "multipart: a failure mid stream aborts the upload",
		file: "internal/runtime/result.go",
		old:  "\t\tif !committed {\n\t\t\tsink.Abort()\n\t\t}",
		new:  "\t\tif !committed {\n\t\t\t_ = sink\n\t\t}",
		pkg:  "./internal/runtime", run: "TestAFailureMidStreamAbortsTheUpload",
	},
	{
		name: "outstream: close commits the block",
		file: "internal/runtime/outstream.go",
		old:  "\to.written = &Written{\n\t\tblock: block,\n\t}\n",
		new:  "\t_ = block\n",
		pkg:  "./internal/runtime", run: "TestTheReferenceMustStillBeReturned",
	},
	{
		name: "outstream: a failed handler uploads nothing",
		file: "internal/runtime/outstream.go",
		old:  "\tif o.written != nil {\n\t\treturn\n\t}\n\n\to.abort()",
		new:  "\tif o.written != nil {\n\t\treturn\n\t}\n\n\t_ = o.Close()",
		pkg:  "./internal/runtime", run: "TestAHandlerThatFailedSendsNothing",
	},
	{
		name: "runner: an unknown category collapses to permanent",
		file: "internal/runtime/errors.go",
		old:  "\tcase PERMANENT, INFRASTRUCTURE, TIMEOUT, EXECUTION:\n\t\treturn true\n\t}\n\n\treturn false",
		new:  "\tcase PERMANENT, INFRASTRUCTURE, TIMEOUT, EXECUTION:\n\t\treturn true\n\t}\n\n\treturn true",
		pkg:  "./internal/runtime", run: "TestAnUnknownCategoryIsReportedAsPermanent",
	},
	{
		name: "runner: a failure without a delay aborts",
		file: "internal/runtime/errors.go",
		old:  "return f.RetryAfter == 0",
		new:  "return false",
		pkg:  "./internal/runtime", run: "TestAFailureWithoutADelayAborts",
	},
	{
		name: "runner: any other error is permanent and aborts",
		file: "internal/runtime/runner.go",
		old:  "\t\t\tRetry: &Retry{\n\t\t\t\tAbort: true,\n\t\t\t},\n",
		new:  "",
		pkg:  "./internal/runtime", run: "TestABugInTheNodeIsPermanentAndNamed",
	},
	{
		name: "runner: a panic becomes an envelope",
		file: "internal/runtime/runner.go",
		old:  "\t\tif recovered := recover(); recovered != nil {",
		new:  "\t\tif recovered := any(nil); recovered != nil {",
		pkg:  "./internal/runtime", run: "TestAPanicInTheNodeIsAFailedEnvelopeNotACrash",
	},
	{
		name: "runner: a failure message is bounded for the transport",
		file: "internal/runtime/runner.go",
		old:  "\tif len(message) <= maxMessageBytes {",
		new:  "\tif len(message) <= maxMessageBytes*1024 {",
		pkg:  "./internal/runtime", run: "TestAFailureMessageIsBoundedForTheTransport",
	},
	{
		name: "runner: the envelope is compact",
		file: "internal/runtime/json.go",
		old:  "\treturn bytes.Clone(out), nil",
		new:  "\treturn append(bytes.Clone(out), '\\n'), nil",
		pkg:  "./internal/runtime", run: "TestTheEnvelopeIsWrittenCompactAndInOrder",
	},
	{
		name: "authoring: a duplicate key is refused",
		file: "internal/authoring/workflow.go",
		old:  "\tif _, taken := wf.handlers[key]; taken {",
		new:  "\tif false {",
		pkg:  "./internal/authoring", run: "TestADuplicateKeyIsRefused",
	},
	{
		name: "authoring: a handle from another workflow is refused",
		file: "internal/authoring/workflow.go",
		old:  "\t\t\tif !slices.Contains(keys, parent) {",
		new:  "\t\t\tif false && !slices.Contains(keys, parent) {",
		pkg:  "./internal/authoring", run: "TestAHandleFromAnotherWorkflowIsRefused",
	},
	{
		name: "authoring: the manifest keeps python's key order",
		file: "internal/authoring/manifest.go",
		old:  "\tV        int               `json:\"v\"`\n\tRuntime  RuntimeManifest   `json:\"runtime\"`\n",
		new:  "\tRuntime  RuntimeManifest   `json:\"runtime\"`\n\tV        int               `json:\"v\"`\n",
		pkg:  ".", run: "TestTheAcceptanceFixtureIsByteStable",
	},
	{
		name: "cli: an unknown command never falls through to invoke",
		file: "internal/cli/cli.go",
		old:  "\tdefault:\n\t\terr = misuse(\"unknown command '%s'\\n\\n%s\", command, help)",
		new:  "\tdefault:\n\t\treturn m.invoke(m.Args)",
		pkg:  ".", run: "TestAnUnknownCommandIsAUsageErrorNotANodeFailure",
	},
	{
		name: "cli: check never writes the file",
		file: "internal/cli/build.go",
		old:  "\t\treturn fail(\"%s does not exist yet, run: %s\", out, regenerate)",
		new:  "\t\tencoded, _ := body.Encode()\n\t\t_ = os.WriteFile(out, encoded, 0o644)\n\t\treturn fail(\"%s does not exist yet, run: %s\", out, regenerate)",
		pkg:  ".", run: "TestCheckDoesNotWriteTheFile",
	},
	{
		name: "cli: dev run keeps the node's own prints",
		file: "internal/cli/dev.go",
		old:  "\treturn m.reportRun(options.node, result, stdout.String(), asJSON)",
		new:  "\treturn m.reportRun(options.node, result, \"\", asJSON)",
		pkg:  ".", run: "TestANodeRunsWithNoHandWrittenEnvelope",
	},
	{
		name: "cli: no hint names a bare dagflows command",
		file: "internal/cli/build.go",
		old:  "\tregenerate := fmt.Sprintf(\"%s build manifest\", invocation())",
		new:  "\tregenerate := \"dagflows build manifest\"",
		pkg:  ".", run: "TestNoHintClaimsABareDagflowsCommand",
	},
}

func main() {
	only := flag.String("only", "", "run guards whose name contains this")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}

	scratch, err := os.MkdirTemp("", "dagflows-guards-")
	if err != nil {
		fatal(err)
	}

	defer os.RemoveAll(scratch)

	if err := copyTree(root, scratch); err != nil {
		fatal(err)
	}

	failed := 0
	ran := 0

	for _, g := range guards {
		if *only != "" && !strings.Contains(g.name, *only) {
			continue
		}

		ran++
		started := time.Now()
		verdict, err := g.check(scratch)
		elapsed := time.Since(started).Round(100 * time.Millisecond)

		if err != nil {
			failed++
			fmt.Printf("WEAK  %-64s %s\n      %v\n", g.name, elapsed, err)
			continue
		}

		fmt.Printf("real  %-64s %s %s\n", g.name, elapsed, verdict)
	}

	fmt.Printf("\n%d guards checked, %d weak\n", ran, failed)

	if failed > 0 || ran == 0 {
		os.Exit(1)
	}
}

// check applies the mutation, runs the named tests, and restores the file.
func (g guard) check(scratch string) (string, error) {
	path := filepath.Join(scratch, filepath.FromSlash(g.file))

	original, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	defer os.WriteFile(path, original, 0o644)

	if n := bytes.Count(original, []byte(g.old)); n != 1 {
		return "", fmt.Errorf("the mutation's anchor appears %d times in %s, want exactly one", n, g.file)
	}

	mutated := bytes.Replace(original, []byte(g.old), []byte(g.new), 1)
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		return "", err
	}

	cmd := exec.Command("go", "test", "-count=1", "-run", "^"+g.run+"$", g.pkg)
	cmd.Dir = scratch
	cmd.Env = append(os.Environ(), "GOWORK=off")

	out, err := cmd.CombinedOutput()
	if err == nil {
		return "", fmt.Errorf("%s stayed green with the bug reintroduced", g.run)
	}

	if bytes.Contains(out, []byte("[build failed]")) || bytes.Contains(out, []byte("setup failed")) {
		return "", fmt.Errorf("the mutation does not compile, so it proves nothing:\n%s", out)
	}

	if !bytes.Contains(out, []byte("--- FAIL: "+g.run)) {
		return "", fmt.Errorf("%s did not run:\n%s", g.run, out)
	}

	return "went red", nil
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, _ := filepath.Rel(from, path)

		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "bin") {
			return filepath.SkipDir
		}

		target := filepath.Join(to, relative)

		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(target, body, 0o644)
	})
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "guards:", err)
	os.Exit(1)
}
