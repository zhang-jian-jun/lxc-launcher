package cmd

import (
	"github.com/urfave/cli/v2"
	"lxc-launcher/log"
	"time"
)

var RootCmd = &cli.App{
	Name:     "launcher",
	HelpName: "launcher",
	Version:  "v0.0.1",
	Usage: `Lxc launcher acts as a lxc instance agent in kubernetes which 
responsible for lxc instance lifecycle management as well as the network proxy`,
	Compiled: time.Now(),
	Authors: []*cli.Author{
		&cli.Author{
			Name:  "TommyLike",
			Email: "tommylikehu@gmail.com",
		},
	},
	Commands: []*cli.Command{
		launchCommand,
		manageCommand,
	},
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "debug",
			Value:   true,
			Usage:   "whether to enable debug log",
			EnvVars: []string{"DEBUG"},
		},
	},
	Before: func(context *cli.Context) error {
		log.InitLog(context.Bool("debug"))
		return nil
	},
}
