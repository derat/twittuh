// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestParseTime(t *testing.T) {
	const layout = "2006-01-02 15:04:05" // defaults to UTC
	now, err := time.Parse(layout, "2020-03-01 03:00:00")
	if err != nil {
		panic(err)
	}

	for _, tc := range []struct {
		ts, want string
	}{
		{"now", "2020-03-01 03:00:00"},
		{"53s", "2020-03-01 02:59:07"},
		{"2m", "2020-03-01 02:58:00"},
		{"23m", "2020-03-01 02:37:00"},
		{"1h", "2020-03-01 02:00:00"},
		{"4h", "2020-02-29 23:00:00"},
		{"23h", "2020-02-29 04:00:00"},
		{"Mar 1", "2020-03-01 12:00:00"}, // probably not actually used
		{"Feb 29", "2020-02-29 12:00:00"},
		{"Dec 14", "2019-12-14 12:00:00"},
		{"Apr 23", "2019-04-23 12:00:00"},
		{"Mar 2", "2019-03-02 12:00:00"},
		{"1 Mar 19", "2019-03-01 12:00:00"},
		{"23 Jan 16", "2016-01-23 12:00:00"},
	} {
		if tt, err := parseTime(tc.ts, now); err != nil {
			t.Errorf("parseTime(%q, %q) failed: %v", tc.ts, now, err)
		} else if got := tt.Format(layout); got != tc.want {
			t.Errorf("parseTime(%q, %q) = %q; want %q", tc.ts, now, got, tc.want)
		}
	}
}

func TestAddLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		orig, want string
	}{
		{"<div></div>", "<div></div>"},
		{"<div>\n</div>", "<div>\n</div>"},
		{"<div>\n\n</div>", "<div>\n\n</div>"},
		{"<div>word</div>", "<div>word</div>"},
		{"<div>word\n</div>", "<div>word<br/></div>"},
		{"<div>two words</div>", "<div>two words</div>"},
		{"<div>first\nsecond</div>", "<div>first<br/>second</div>"},
		{"<div>double\n\nbreak</div>", "<div>double<br/><br/>break</div>"},
		{"<div>\nleading\nand\ntrailing\n</div>", "<div><br/>leading<br/>and<br/>trailing<br/></div>"},
		{"<div> 1\n2</div> \n <div>3\n4 </div>", "<div> 1<br/>2</div> \n <div>3<br/>4 </div>"},
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
