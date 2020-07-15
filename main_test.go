// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gorilla/feeds"
)

func TestE2E(t *testing.T) {
	const (
		user       = "librarycongress"
		pageDir    = "testdata/pages"
		goldenPath = "testdata/librarycongress.json"
		numPages   = 3
	)

	ft, err := newFetcher(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	ft.forTest = true // disallow network access

	// Generate the feed.
	prof, tweets, latestID, err := getTimeline(ft, user, 0 /* oldLatestID */, numPages, true /* embeds */)
	if err != nil {
		t.Fatalf("getTimeline(ft, %q, ...) failed: %v", user, err)
	}
	var out bytes.Buffer
	if err := writeFeed(&out, jsonFormat, prof, tweets, latestID, false /* replies */); err != nil {
		t.Fatal("writeFeed(...) failed: ", err)
	}
	var got feeds.JSONFeed
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal("Failed unmarshaling generated feed: ", err)
	}

	// Read the golden version of the feed.
	b, err := ioutil.ReadFile(goldenPath)
	if err != nil {
		t.Fatal("Failed reading golden file: ", err)
	}
	var want feeds.JSONFeed
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatalf("Failed unmarshaling %v: %v", goldenPath, err)
	}

	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(feeds.JSONItem{}, "PublishedDate", "ModifiedDate")); diff != "" {
		t.Errorf("Didn't get expected feed:\n%v", diff)
	}
}
