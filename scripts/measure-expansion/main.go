// Command measure-expansion measures how many bytes of heap a decoded reference retains per byte of JSON.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
)

type shape struct {
	name string
	gen  func(records int) []byte
}

var shapes = []shape{
	{
		name: "small objects",
		gen: func(n int) []byte {
			return ndjsonOf(n, func(i int) any {
				return map[string]any{
					"id":   i,
					"name": fmt.Sprintf("n%d", i),
					"ok":   i%2 == 0,
				}
			})
		},
	},
	{
		name: "ten short fields",
		gen: func(n int) []byte {
			return ndjsonOf(n, func(i int) any {
				row := map[string]any{}

				for f := range 10 {
					row[fmt.Sprintf("f%d", f)] = i*10 + f
				}

				return row
			})
		},
	},
	{
		name: "many distinct short keys",
		gen: func(n int) []byte {
			return ndjsonOf(n, func(i int) any {
				row := map[string]any{}

				for f := range 10 {
					row[fmt.Sprintf("k%d_%d", i, f)] = f
				}

				return row
			})
		},
	},
	{
		name: "flat array of numbers",
		gen: func(n int) []byte {
			items := make([]any, n)

			for i := range items {
				items[i] = i * 7
			}

			return jsonOf(items)
		},
	},
	{
		name: "flat array of short strings",
		gen: func(n int) []byte {
			items := make([]any, n)

			for i := range items {
				items[i] = fmt.Sprintf("s%d", i)
			}

			return jsonOf(items)
		},
	},
	{
		name: "nested records",
		gen: func(n int) []byte {
			return ndjsonOf(n, func(i int) any {
				return map[string]any{
					"id":    i,
					"email": fmt.Sprintf("user%d@example.com", i),
					"user": map[string]any{
						"name": "ana",
						"address": map[string]any{
							"city": "x",
							"zip":  "1",
						},
					},
					"tags": []any{"a", "b"},
				}
			})
		},
	},
	{
		name: "deeply nested",
		gen: func(n int) []byte {
			return ndjsonOf(n, func(i int) any {
				var v any = i

				for range 6 {
					v = map[string]any{
						"n": []any{v},
					}
				}

				return v
			})
		},
	},
	{
		name: "one long string per record",
		gen: func(n int) []byte {
			rng := rand.New(rand.NewPCG(1, 2))

			return ndjsonOf(n, func(i int) any {
				return map[string]any{
					"id":   i,
					"text": strings.Repeat(string(rune('a'+rng.IntN(26))), 200),
				}
			})
		},
	},
	{
		name: "one big string",
		gen: func(n int) []byte {
			return jsonOf(strings.Repeat("x", n*100))
		},
	},
}

func ndjsonOf(n int, row func(int) any) []byte {
	var out bytes.Buffer

	enc := json.NewEncoder(&out)

	for i := range n {
		enc.Encode(row(i))
	}

	return out.Bytes()
}

func jsonOf(v any) []byte {
	raw, _ := json.Marshal(v)

	return raw
}

func decode(raw []byte) []any {
	var out []any

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	for dec.More() {
		var v any

		if err := dec.Decode(&v); err != nil {
			panic(err)
		}

		out = append(out, v)
	}

	return out
}

func heap() uint64 {
	runtime.GC()
	runtime.GC()

	var stats runtime.MemStats

	runtime.ReadMemStats(&stats)

	return stats.HeapAlloc
}

func main() {
	fmt.Printf("%s %s/%s\n\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Printf("| %-28s | %8s | %12s | %9s |\n", "shape", "records", "input bytes", "expansion")
	fmt.Printf("|%s|%s|%s|%s|\n", strings.Repeat("-", 30), strings.Repeat("-", 10), strings.Repeat("-", 14), strings.Repeat("-", 11))

	worst := 0.0

	for _, s := range shapes {
		for _, records := range []int{1_000, 10_000, 50_000} {
			raw := s.gen(records)
			before := heap()
			held := decode(raw)
			after := heap()
			ratio := float64(max(int64(after)-int64(before), 0)) / float64(len(raw))
			runtime.KeepAlive(held)
			worst = max(worst, ratio)

			fmt.Printf("| %-28s | %8d | %12d | %8.2fx |\n", s.name, records, len(raw), ratio)
		}
	}

	fmt.Printf("\nworst shape: %.2fx\n", worst)
}
