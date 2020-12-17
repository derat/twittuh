// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

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
