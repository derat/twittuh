// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"golang.org/x/net/html"
)

var updateGolden = flag.Bool("update-golden", false, "Update parse tests' golden files")

func TestParseTimeline(t *testing.T) {
	fns, err := filepath.Glob("testdata/*.html")
	if err != nil {
		t.Fatal("Failed globbing HTML files: ", err)
	}
	for _, fn := range fns {
		df, err := os.Open(fn)
		if err != nil {
			t.Fatal("Failed opening HTML file: ", err)
		}
		defer df.Close()

		prof, tweets, err := parseTimeline(df)
		if err != nil {
			t.Errorf("Failed parsing %v: %v", fn, err)
			continue
		}

		// Golden files.
		pfn := fn[:len(fn)-5] + "-profile.json"
		tfn := fn[:len(fn)-5] + "-tweets.json"

		if *updateGolden {
			if err := writeJSONFile(pfn, prof); err != nil {
				t.Fatal("Failed writing golden profile: ", err)
			}
			if err := writeJSONFile(tfn, tweets); err != nil {
				t.Fatal("Failed writing golden tweets: ", err)
			}
		} else {
			var gp profile
			if err := readJSONFile(pfn, &gp); err != nil {
				t.Fatal("Failed reading golden profile: ", err)
			}
			if diff := cmp.Diff(gp, prof); diff != "" {
				t.Errorf("Didn't get expected profile from %v:\n%v", fn, diff)
			}
			var gt []tweet
			if err := readJSONFile(tfn, &gt); err != nil {
				t.Fatal("Failed reading golden tweets: ", err)
			}
			if diff := cmp.Diff(gt, tweets); diff != "" {
				t.Errorf("Didn't get expected tweets from %v:\n%v", fn, diff)
			}
		}
	}
}

// writeJSONFile marshals v to JSON and writes it to fn.
// It disables HTML escaping in the generated JSON.
func writeJSONFile(fn string, v interface{}) error {
	f, err := os.Create(fn)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readJSONFile reads JSON data from fn and unmarshals it into dst.
func readJSONFile(fn string, dst interface{}) error {
	f, err := os.Open(fn)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(dst)
}

func TestAddLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		orig, want string
	}{
		{"<div></div>", "<div></div>"},
		{"<div>\n</div>", "<div><br/>\n</div>"},
		{"<div>\n\n</div>", "<div><br/>\n<br/>\n</div>"},
		{"<div>word</div>", "<div>word</div>"},
		{"<div>word\n</div>", "<div>word<br/>\n</div>"},
		{"<div>two words</div>", "<div>two words</div>"},
		{"<div>first\nsecond</div>", "<div>first<br/>\nsecond</div>"},
		{"<div>double\n\nbreak</div>", "<div>double<br/>\n<br/>\nbreak</div>"},
		{"<div>\nleading\nand\ntrailing\n</div>", "<div><br/>\nleading<br/>\nand<br/>\ntrailing<br/>\n</div>"},
		{"<div> 1\n2</div> \n <div>3\n4 </div>", "<div> 1<br/>\n2</div> <br/>\n <div>3<br/>\n4 </div>"},
	} {
		root, err := html.Parse(strings.NewReader(tc.orig))
		if err != nil {
			t.Fatalf("Failed parsing %q: %v", tc.orig, err)
		}

		addLineBreaks(root)

		// Render back to a string.
		var b bytes.Buffer
		if err := html.Render(&b, root); err != nil {
			t.Fatal("Failed rendering tree: ", err)
		}

		// Drop uninteresting elements.
		got := b.String()
		got = strings.TrimPrefix(got, "<html><head></head><body>")
		got = strings.TrimSuffix(got, "</body></html>")

		if got != tc.want {
			t.Errorf("addLineBreaks(%q) = %q; want %q", tc.orig, got, tc.want)
		}
	}
}
