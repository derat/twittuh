// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flag]... <USER> <FILE>\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Creates an RSS feed from a Twitter user's timeline.\n")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}
	df := flag.String("debug-file", "", "HTML file to parse instead of downloading timeline (for debugging)")
	flag.Parse()

	if *df != "" {
		f, err := os.Open(*df)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Failed opening file: ", err)
			os.Exit(1)
		}
		defer f.Close()

		tweets, err := parse(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Failed parsing timeline: ", err)
			os.Exit(1)
		}

		for _, t := range tweets {
			fmt.Printf("%+v\n", t)
		}
		os.Exit(0)
	}

	if len(flag.Args()) != 2 {
		flag.Usage()
		os.Exit(2)
	}

	// TODO: Fetch timeline and write feed.
	user := flag.Arg(0)
	if len(user) > 0 && user[0] == '@' {
		user = user[1:]
	}
	//feed := flag.Arg(1)
}
