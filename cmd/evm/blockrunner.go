// Copyright 2023 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/Sakura2598/go-ribble/core"
	"github.com/Sakura2598/go-ribble/core/rawdb"
	"github.com/Sakura2598/go-ribble/core/tracing"
	"github.com/Sakura2598/go-ribble/eth/tracers/logger"
	"github.com/Sakura2598/go-ribble/tests"
	"github.com/urfave/cli/v2"
)

var RunFlag = &cli.StringFlag{
	Name:  "run",
	Value: ".*",
	Usage: "Run only those tests matching the regular expression.",
}

var blockTestCommand = &cli.Command{
	Action:    blockTestCmd,
	Name:      "blocktest",
	Usage:     "Executes the given blockchain tests",
	ArgsUsage: "<file>",
	Flags:     []cli.Flag{RunFlag},
}

func blockTestCmd(ctx *cli.Context) error {
	if len(ctx.Args().First()) == 0 {
		return errors.New("path-to-test argument required")
	}

	var tracer *tracing.Hooks
	// Configure the EVM logger
	if ctx.Bool(MachineFlag.Name) {
		tracer = logger.NewJSONLogger(&logger.Config{
			EnableMemory:     !ctx.Bool(DisableMemoryFlag.Name),
			DisableStack:     ctx.Bool(DisableStackFlag.Name),
			DisableStorage:   ctx.Bool(DisableStorageFlag.Name),
			EnableReturnData: !ctx.Bool(DisableReturnDataFlag.Name),
		}, os.Stderr)
	}
	// Load the test content from the input file
	src, err := os.ReadFile(ctx.Args().First())
	if err != nil {
		return err
	}
	var tests map[string]tests.BlockTest
	if err = json.Unmarshal(src, &tests); err != nil {
		return err
	}
	re, err := regexp.Compile(ctx.String(RunFlag.Name))
	if err != nil {
		return fmt.Errorf("invalid regex -%s: %v", RunFlag.Name, err)
	}

	// Run them in order
	var keys []string
	for key := range tests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if !re.MatchString(name) {
			continue
		}
		test := tests[name]
		if err := test.Run(false, rawdb.HashScheme, false, tracer, func(res error, chain *core.BlockChain) {
			if ctx.Bool(DumpFlag.Name) {
				if state, _ := chain.State(); state != nil {
					fmt.Println(string(state.Dump(nil)))
				}
			}
		}); err != nil {
			return fmt.Errorf("test %v: %w", name, err)
		}
	}
	return nil
}
