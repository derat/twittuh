// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/feeds"
)

const (
	baseFetchURL = "https://mobile.twitter.com/"
	titleLen     = 80
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flag]... <USER> <FILE>\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Creates an RSS feed from a Twitter user's timeline.\n")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}
	debugFile := flag.String("debug-file", "", "HTML timeline file to parse for debugging")
	maxRequests := flag.Int("max-requests", 3, "Maximum number of HTTP requests to make to Twitter")
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
	user := bareUser(flag.Arg(0))
	feedPath := flag.Arg(1)

	oldMaxID := int64(0) // TODO: Figure out which tweets we should get.
	tweets, err := fetch(user, oldMaxID, *maxRequests)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed getting tweets for %v: %v\n", user, err)
		os.Exit(1)
	}
	feed := makeFeed(tweets, user, *replies)

	f, err := os.Create(feedPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed creating feed: ", err)
		os.Exit(1)
	}
	// TODO: Support RSS vs. Atom vs. JSON Feed.
	if err := feed.WriteAtom(f); err != nil {
		f.Close()
		fmt.Fprintln(os.Stderr, "Failed writing feed: ", err)
		os.Exit(1)
	}
	// TODO: Write trailing comment with max ID, maybe? Need to do something else for JSON, though.
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "Failed closing feed: ", err)
		os.Exit(1)
	}
}

// fetch downloads and returns tweets from the supplied user's timeline.
// At most maxRequests will be issued to Twitter. Tweets newer then oldMaxID
// will be returned if possible. Some number of additional tweets older than
// it may also be returned.
func fetch(user string, oldMaxID int64, maxRequests int) ([]tweet, error) {
	f := func(url string) ([]tweet, error) {
		resp, err := http.Get(url)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return parse(resp.Body)
	}

	var tweets []tweet

	baseURL := baseFetchURL + user
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

// makeFeed returns a format-agnostic feed containing the supplied tweets from the supplied
// user's timeline. If replies is true, the user's replies will also be included.
func makeFeed(tweets []tweet, user string, replies bool) *feeds.Feed {
	// Try to find the user's name from one of the tweets.
	author := "@" + user
	for _, t := range tweets {
		if t.user == user {
			author = t.displayName()
			break
		}
	}

	descPre := "Tweets"
	if replies {
		descPre += " and replies"
	}

	feed := &feeds.Feed{
		Title:       author,
		Link:        &feeds.Link{Href: userURL(user)},
		Description: fmt.Sprintf("%s from @%v's timeline", descPre, user),
		Author:      &feeds.Author{Name: author},
		Updated:     time.Now(),
		Copyright:   fmt.Sprintf("©%v %v", time.Now().Year(), author),
	}

	for _, t := range tweets {
		if !replies && t.reply() {
			continue
		}
		title := t.text
		if len(title) > titleLen {
			title = title[:titleLen-1] + "…"
		}
		feed.Add(&feeds.Item{
			Title:       title,
			Link:        &feeds.Link{Href: t.href}, // Atom's default rel is "alternate"
			Description: t.text,
			Author:      &feeds.Author{Name: t.displayName()},
			Id:          fmt.Sprintf("%v", t.id),
			Created:     t.time,
			Content:     t.content,
		})
	}

	return feed
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
