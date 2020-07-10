// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
)

const urlPrefix = "https://mobile.twitter.com/"

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flag]... <USER> <FILE>\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Creates an RSS feed from a Twitter user's timeline.\n")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}
	debugFile := flag.String("debug-file", "", "HTML file to parse instead of downloading timeline (for debugging)")
	maxRequests := flag.Int("max-requests", 3, "Maximum number of pages to request from Twitter")
	replies := flag.Bool("replies", false, "Include the user's replies")
	flag.Parse()

	if *debugFile != "" {
		if err := debug(*debugFile, *replies); err != nil {
			fmt.Fprintln(os.Stderr, "Failed reading timeline: ", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(flag.Args()) != 2 {
		flag.Usage()
		os.Exit(2)
	}

	user := flag.Arg(0)
	if len(user) > 0 && user[0] == '@' {
		user = user[1:]
	}

	feed := flag.Arg(1)
	f, err := os.Create(feed)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed creating file: ", err)
		os.Exit(1)
	}
	defer f.Close() // TODO: Check error.

	url := urlPrefix + user
	oldMaxID := int64(0) // TODO: Figure out which tweets we should get.
	tweets, err := fetch(url, oldMaxID, *maxRequests)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed getting tweets from %v: %v\n", url, err)
		os.Exit(1)
	}

	// TODO: Write feed.
	for _, t := range tweets {
		if *replies || !t.reply() {
			fmt.Printf("%+v\n", t)
		}
	}
}

func fetch(baseURL string, oldMaxID int64, maxRequests int) ([]tweet, error) {
	f := func(url string) ([]tweet, error) {
		resp, err := http.Get(url)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return parse(resp.Body)
	}

	var tweets []tweet

	url := baseURL
	for nr := 0; nr < maxRequests; nr++ {
		newTweets, err := f(url)
		if err != nil {
			return tweets, err
		} else if len(newTweets) == 0 {
			break
		}

		tweets = append(tweets, newTweets...)

		if minID := newTweets[len(newTweets)-1].id; minID <= oldMaxID+1 {
			break
		} else {
			url = fmt.Sprintf("%s?max_id=%v", baseURL, minID-1)
		}
	}

	// TODO: Warn if there's a potential gap because we ran out of requests, maybe.
	return tweets, nil
}

// debug reads an HTML timeline from p and dumps its tweets to stdout.
func debug(p string, replies bool) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	tweets, err := parse(f)
	if err != nil {
		return err
	}

	for _, t := range tweets {
		if replies || !t.reply() {
			fmt.Printf("%+v\n", t)
		}
	}
	return nil
}
